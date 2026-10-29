package main

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"openuai/internal/config"
	"openuai/internal/llm"
)

func TestSaveAndCheckAttachments(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg}
	att, err2 := a.SaveAttachment("../../etc/evil name.txt", base64.StdEncoding.EncodeToString([]byte("hello")))
	err = err2
	if err != nil {
		t.Fatal(err)
	}
	if att.Name != "evil name.txt" || att.Size != 5 || !strings.HasPrefix(att.Path, a.attachmentsDir()) {
		t.Fatalf("bad attachment: %+v", att)
	}
	if b, _ := os.ReadFile(att.Path); string(b) != "hello" {
		t.Fatal("content not saved")
	}
	if _, err := a.checkAttachments([]llm.Attachment{att}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.checkAttachments([]llm.Attachment{{Name: "p", Path: "/etc/passwd"}}); err == nil {
		t.Fatal("files outside the attachments folder must be refused")
	}
}
