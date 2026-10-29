//go:build !windows

package subagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openuai/internal/hooks"
)

func TestWorktreeHooksReplaceGit(t *testing.T) {
	repo := newRepo(t)
	base := t.TempDir()
	log := filepath.Join(base, "log")
	hs := &hooks.Set{}
	hs.Add(hooks.Config{
		hooks.WorktreeCreate: {{Hooks: []hooks.Handler{{Type: "command", Command: `n=$(jq -r .name); mkdir -p "` + base + `/$n" && echo "created $n" >&2 && echo "` + base + `/$n"`}}}},
		hooks.WorktreeRemove: {{Hooks: []hooks.Handler{{Type: "command", Command: `p=$(jq -r .worktree_path); echo "remove $p" >> "` + log + `"; rm -rf "$p"`}}}},
	}, "test")
	w, err := CreateWorktreeWithHooks(repo, "my task", hs, map[string]any{"session_id": "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.Dir, base+"/my-task-") || !strings.Contains(IsolationPrompt(w), "WorktreeCreate hook") {
		t.Fatalf("dir %s", w.Dir)
	}
	if kept, note := w.Finish(); kept || !strings.Contains(note, "WorktreeRemove") {
		t.Fatalf("kept=%v %s", kept, note)
	}
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		t.Fatal("the WorktreeRemove hook must have removed it")
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "remove "+w.Dir) {
		t.Fatalf("log %q", b)
	}

	// Without WorktreeRemove the hook-made worktree is kept (as Claude Code).
	only := &hooks.Set{}
	only.Add(hooks.Config{hooks.WorktreeCreate: {{Hooks: []hooks.Handler{hs.Entries[0].H}}}}, "x")
	w, err = CreateWorktreeWithHooks(repo, "keep", only, nil)
	if err != nil {
		t.Fatal(err)
	}
	if kept, _ := w.Finish(); !kept {
		t.Fatal("must be kept without a WorktreeRemove hook")
	}

	// A failing WorktreeCreate fails the isolation.
	bad := &hooks.Set{}
	bad.Add(hooks.Config{hooks.WorktreeCreate: {{Hooks: []hooks.Handler{{Type: "command", Command: "echo nope >&2; exit 1"}}}}}, "test")
	if _, err := CreateWorktreeWithHooks(repo, "x", bad, nil); err == nil || !strings.Contains(err.Error(), "WorktreeCreate") {
		t.Fatalf("err %v", err)
	}
}
