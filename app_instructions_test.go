//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitLines waits until p has at least n lines.
func waitLines(t *testing.T, p string, n int) []string {
	t.Helper()
	var lines []string
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(p); err == nil {
			lines = strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) >= n {
				return lines
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: want %d lines, got %q", p, n, lines)
	return nil
}

func TestMainConversationInstructions(t *testing.T) {
	log := filepath.Join(t.TempDir(), "loaded.log")
	a, work := hookApp(t, `{"hooks":{"InstructionsLoaded":[{"hooks":[{"type":"command","command":"jq -r '.file_path + \" \" + .memory_type + \" \" + .load_reason' >> `+log+`"}]}]}}`)
	// Without any AGENTS.md nothing is added and no hook fires.
	if got := a.instructionsLoader()(); got != "" {
		t.Fatalf("no instruction files: %q", got)
	}
	if len(a.GetInstructionFiles()) != 0 {
		t.Fatal("settings list must be empty")
	}

	// Repository root, a nested working directory and the user's file.
	root := work
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(a.cfg.ConfigDir(), "AGENTS.md")
	for p, c := range map[string]string{userFile: "USER_RULE", filepath.Join(root, "AGENTS.md"): "ROOT_RULE", filepath.Join(sub, "AGENTS.md"): "SUB_RULE"} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(sub)

	load := a.instructionsLoader() // a new conversation
	p := load()
	u, r, s := strings.Index(p, "USER_RULE"), strings.Index(p, "ROOT_RULE"), strings.Index(p, "SUB_RULE")
	if u < 0 || !(u < r && r < s) {
		t.Fatalf("order must be user, root, nested: %q", p)
	}
	files := a.GetInstructionFiles()
	if len(files) != 3 || files[0].Scope != "user" || files[2].Path != filepath.Join(sub, "AGENTS.md") {
		t.Fatalf("settings list: %+v", files)
	}
	lines := waitLines(t, log, 3)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{userFile + " User session_start", filepath.Join(root, "AGENTS.md") + " Project session_start", filepath.Join(sub, "AGENTS.md") + " Project session_start"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("hook lines %q miss %q", lines, want)
		}
	}

	// Unchanged: same prompt, no new hook.
	if again := load(); again != p {
		t.Fatal("unchanged files must give the same section")
	}
	// Edited: the new text, and the hook fires for that file only.
	if err := os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("SUB_RULE_2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p2 := load(); !strings.Contains(p2, "SUB_RULE_2") {
		t.Fatalf("edit not applied: %q", p2)
	}
	lines = waitLines(t, log, 4)
	time.Sleep(200 * time.Millisecond)
	lines = waitLines(t, log, 4)
	if len(lines) != 4 || lines[3] != filepath.Join(sub, "AGENTS.md")+" Project file_changed" {
		t.Fatalf("hook lines after the edit: %q", lines)
	}
}
