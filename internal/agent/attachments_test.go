package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openuai/internal/llm"
)

func TestComposeWithAttachments(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(txt, []byte("hola ATTACH75\n"), 0o600)
	bin := filepath.Join(dir, "x.pdf")
	_ = os.WriteFile(bin, []byte("%PDF\x00\x01binary"), 0o600)
	files := []llm.Attachment{{Name: "notes.txt", Path: txt, Size: 14, Mime: "text/plain"}, {Name: "x.pdf", Path: bin, Size: 12, Mime: "application/pdf"}}
	c := ComposeWithAttachments("resume esto", files)
	for _, want := range []string{"resume esto", AttachedFilesMarker, txt, bin, "ATTACH75"} {
		if !strings.Contains(c, want) {
			t.Fatalf("missing %q in %s", want, c)
		}
	}
	if strings.Contains(c, "binary") {
		t.Fatal("binary files must not be inlined")
	}
	if got := DisplayContent(llm.Message{Content: c, Attachments: files}); got != "resume esto" {
		t.Fatalf("display: %q", got)
	}
	a := &Agent{messages: []llm.Message{{Role: llm.RoleSystem}}}
	a.BeginWithAttachments("x", nil, files)
	if len(a.UserMessageAttachments(0)) != 2 {
		t.Fatal("attachments not kept")
	}
}
