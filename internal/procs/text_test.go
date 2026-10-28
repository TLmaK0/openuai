package procs

import (
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	cases := map[string]string{
		"\x1b[31mred\x1b[0m\r\nab\rcd\n": "red\ncd\n",
		"abc\b\bX":                       "aX",
		"\x1b]0;title\x07plain":          "plain",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedact(t *testing.T) {
	for _, s := range []string{
		"token ghp_abcdefghijklmnopqrstuvwxyz1234",
		"key sk-abcdefghijklmnopqrstuvwxyz",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz",
		"password=supersecret123",
	} {
		got := Redact(s)
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("Redact(%q) = %q", s, got)
		}
	}
	if got := Redact("nothing secret here"); got != "nothing secret here" {
		t.Errorf("unexpected change: %q", got)
	}
}

func TestLooksLikePrompt(t *testing.T) {
	yes := []string{
		"Password: ",
		"Overwrite file? [Y/n] ",
		"Enter passphrase for key '/home/x/.ssh/id_ed25519': ",
		"? Where do you use GitHub?  [Use arrows to move]\n> GitHub.com\n  other",
		"Press Enter to open github.com in your browser...",
	}
	for _, s := range yes {
		if !LooksLikePrompt(s) {
			t.Errorf("expected prompt: %q", s)
		}
	}
	no := []string{"Done.", "", "  VITE ready in 300 ms\n  Local: http://localhost:5173/"}
	for _, s := range no {
		if LooksLikePrompt(s) {
			t.Errorf("unexpected prompt: %q", s)
		}
	}
}

func TestRingBuffer(t *testing.T) {
	p := &Proc{}
	p.appendLocked(make([]byte, BufferSize-10))
	p.appendLocked(make([]byte, 100))
	if len(p.buf) != BufferSize || p.total != BufferSize+90 {
		t.Fatalf("len=%d total=%d", len(p.buf), p.total)
	}
	if p.startOffsetLocked() != 90 {
		t.Fatalf("start=%d", p.startOffsetLocked())
	}
}
