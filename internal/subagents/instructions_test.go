package subagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInstructionsOrder(t *testing.T) {
	cfg := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "pkg")
	user := UserInstructionsFile(cfg)
	write(t, user, "user rules")
	write(t, filepath.Join(root, "AGENTS.md"), "root rules")
	write(t, filepath.Join(sub, "AGENTS.md"), "sub rules")

	files := LoadInstructions(user, sub)
	if len(files) != 3 {
		t.Fatalf("files: %+v", files)
	}
	want := []struct{ path, scope string }{
		{user, ScopeUser}, {filepath.Join(root, "AGENTS.md"), ScopeProject}, {filepath.Join(sub, "AGENTS.md"), ScopeProject},
	}
	for i, w := range want {
		if files[i].Path != w.path || files[i].Scope != w.scope {
			t.Fatalf("file %d = %+v, want %v", i, files[i], w)
		}
	}
	p := MainInstructionsPrompt(files)
	u, r, s := strings.Index(p, "user rules"), strings.Index(p, "root rules"), strings.Index(p, "sub rules")
	if u < 0 || r < 0 || s < 0 || !(u < r && r < s) {
		t.Fatalf("order must be user, root, closest: %q", p)
	}

	// Outside the repository root only the user file and workDir count.
	if got := LoadInstructions("", t.TempDir()); len(got) != 0 {
		t.Fatalf("no AGENTS.md: %+v", got)
	}
	if MainInstructionsPrompt(nil) != "" {
		t.Fatal("no files must add nothing to the prompt")
	}
	// A missing user file is skipped.
	if got := LoadInstructions(filepath.Join(cfg, "none", "AGENTS.md"), sub); len(got) != 2 {
		t.Fatalf("missing user file: %+v", got)
	}
}

func TestLargeInstructionsAreCappedWithNote(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("é", maxInstructionBytes) // 2 bytes each
	write(t, filepath.Join(dir, "AGENTS.md"), big)
	files := LoadInstructions("", dir)
	if len(files) != 1 || !files[0].Truncated || files[0].Size != len(big) {
		t.Fatalf("files: %+v", files)
	}
	c := files[0].Content
	if !strings.Contains(c, "[Truncated: this file is") || len(c) > maxInstructionBytes+200 {
		t.Fatalf("capped content must end with a visible note (len %d)", len(c))
	}
	if strings.ContainsRune(c, '�') {
		t.Fatal("cut in the middle of a character")
	}
	if !strings.Contains(ProjectInstructions(dir), "[Truncated:") {
		t.Fatal("sub-agents get the same note")
	}
}
