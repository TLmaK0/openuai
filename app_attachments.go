package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"openuai/internal/llm"
)

// Limits for files attached to a message.
const (
	maxFilesPerMessage = 10
	maxAttachmentBytes = 50 << 20
)

// attachmentsDir is where attached files are kept, so they stay available
// after a restart and the assistant can open them by path.
func (a *App) attachmentsDir() string {
	return filepath.Join(a.cfg.ConfigDir(), "attachments")
}

// SaveAttachment stores a file dropped into the message box (base64 content)
// and returns its attachment record.
func (a *App) SaveAttachment(name, dataB64 string) (llm.Attachment, error) {
	if base64.StdEncoding.DecodedLen(len(dataB64)) > maxAttachmentBytes+3 {
		return llm.Attachment{}, fmt.Errorf("%s is too large: at most %d MB per file", name, maxAttachmentBytes>>20)
	}
	data, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return llm.Attachment{}, fmt.Errorf("%s could not be read", name)
	}
	return a.storeAttachment(name, func(w io.Writer) error { _, err := w.Write(data); return err })
}

// PickAttachments opens the system file picker and stores the chosen files.
func (a *App) PickAttachments() ([]llm.Attachment, error) {
	paths, err := wailsRuntime.OpenMultipleFilesDialog(a.ctx, wailsRuntime.OpenDialogOptions{Title: "Attach files"})
	if err != nil {
		return nil, err
	}
	var out []llm.Attachment
	var errs []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			errs = append(errs, filepath.Base(p)+" is not a file")
			continue
		}
		if st.Size() > maxAttachmentBytes {
			errs = append(errs, fmt.Sprintf("%s is too large: at most %d MB per file", filepath.Base(p), maxAttachmentBytes>>20))
			continue
		}
		att, err := a.storeAttachment(filepath.Base(p), func(w io.Writer) error {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(w, f)
			return err
		})
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		out = append(out, att)
	}
	if len(errs) > 0 {
		return out, errors.New(strings.Join(errs, "; "))
	}
	return out, nil
}

func (a *App) storeAttachment(name string, write func(io.Writer) error) (llm.Attachment, error) {
	name = safeFileName(name)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	dir := filepath.Join(a.attachmentsDir(), time.Now().Format("20060102-150405")+"-"+hex.EncodeToString(b))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return llm.Attachment{}, err
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return llm.Attachment{}, err
	}
	if err := write(f); err != nil {
		f.Close()
		os.RemoveAll(dir)
		return llm.Attachment{}, fmt.Errorf("%s could not be saved: %v", name, err)
	}
	if err := f.Close(); err != nil {
		return llm.Attachment{}, err
	}
	return describeAttachment(path, name)
}

func describeAttachment(path, name string) (llm.Attachment, error) {
	st, err := os.Stat(path)
	if err != nil {
		return llm.Attachment{}, err
	}
	m := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if m == "" {
		if f, err := os.Open(path); err == nil {
			head := make([]byte, 512)
			n, _ := f.Read(head)
			f.Close()
			m = http.DetectContentType(head[:n])
		}
	}
	if i := strings.Index(m, ";"); i > 0 {
		m = m[:i]
	}
	return llm.Attachment{Name: name, Path: path, Size: st.Size(), Mime: m}, nil
}

// checkAttachments accepts only files stored by SaveAttachment/PickAttachments
// (inside the attachments folder) and refreshes their details.
func (a *App) checkAttachments(files []llm.Attachment) ([]llm.Attachment, error) {
	if len(files) > maxFilesPerMessage {
		return nil, fmt.Errorf("Too many files: at most %d per message.", maxFilesPerMessage)
	}
	root := a.attachmentsDir()
	out := make([]llm.Attachment, 0, len(files))
	for _, f := range files {
		p := filepath.Clean(f.Path)
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("%s is not an attached file", f.Name)
		}
		att, err := describeAttachment(p, filepath.Base(p))
		if err != nil {
			return nil, fmt.Errorf("%s is no longer available; attach it again", f.Name)
		}
		out = append(out, att)
	}
	return out, nil
}

// safeFileName keeps the base name of a file, without path separators or
// control characters.
func safeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == ':' {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	if len(name) > 120 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:120-len(ext)] + ext
	}
	return name
}
