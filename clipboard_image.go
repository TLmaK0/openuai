package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// maxClipboardImage bounds what is read from the clipboard (raw bytes).
const maxClipboardImage = 40 << 20

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// ClipboardImage returns the image currently in the system clipboard as
// base64 PNG, or "" when the clipboard holds no image.
//
// The webview cannot always see clipboard images (WebKitGTK gives the paste
// event no data for them), so the UI asks here when a paste brings nothing.
func (a *App) ClipboardImage() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := readClipboardPNG(ctx)
	if err != nil || len(data) == 0 {
		return "", err
	}
	if len(data) > maxClipboardImage {
		return "", errors.New("the image in the clipboard is too large")
	}
	if !bytes.HasPrefix(data, pngMagic) {
		return "", nil
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func readClipboardPNG(ctx context.Context) ([]byte, error) {
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			if _, err := exec.LookPath("wl-paste"); err == nil {
				if !clipboardOffers(ctx, "image/png", "wl-paste", "--list-types") {
					return nil, nil
				}
				return exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", "image/png").Output()
			}
		}
		if _, err := exec.LookPath("xclip"); err != nil {
			return nil, errors.New("pasting images needs xclip (X11) or wl-clipboard (Wayland) installed")
		}
		if !clipboardOffers(ctx, "image/png", "xclip", "-selection", "clipboard", "-o", "-t", "TARGETS") {
			return nil, nil
		}
		return exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-o", "-t", "image/png").Output()
	case "darwin":
		out, err := exec.CommandContext(ctx, "osascript", "-e", "the clipboard as «class PNGf»").Output()
		if err != nil {
			return nil, nil // no image in the clipboard
		}
		return parseAppleScriptData(string(out))
	case "windows":
		tmp := filepath.Join(os.TempDir(), "openuai-clipboard.png")
		defer os.Remove(tmp)
		script := `Add-Type -AssemblyName System.Windows.Forms; $i=[System.Windows.Forms.Clipboard]::GetImage(); if ($i) { $i.Save('` + tmp + `', [System.Drawing.Imaging.ImageFormat]::Png) }`
		if err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-STA", "-Command", script).Run(); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(tmp)
		if os.IsNotExist(err) {
			return nil, nil
		}
		return data, err
	}
	return nil, nil
}

func clipboardOffers(ctx context.Context, mime string, name string, args ...string) bool {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == mime {
			return true
		}
	}
	return false
}

var appleData = regexp.MustCompile(`«data PNGf([0-9A-Fa-f]+)»`)

// parseAppleScriptData decodes osascript's «data PNGf…» hex output.
func parseAppleScriptData(s string) ([]byte, error) {
	m := appleData.FindStringSubmatch(s)
	if m == nil {
		return nil, nil
	}
	return hex.DecodeString(m[1])
}
