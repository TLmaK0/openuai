package subagents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"openuai/internal/tools"
)

func TestResolveDir(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "proj")
	_ = os.Mkdir(sub, 0o755)
	file := filepath.Join(base, "f.txt")
	_ = os.WriteFile(file, []byte("x"), 0o644)

	if d, err := ResolveDir(sub, "/elsewhere"); err != nil || d != sub {
		t.Fatalf("absolute: %q %v", d, err)
	}
	if d, err := ResolveDir("proj", base); err != nil || d != sub {
		t.Fatalf("relative: %q %v", d, err)
	}
	home, _ := os.UserHomeDir()
	if d, err := ResolveDir("~", base); err != nil || d != filepath.Clean(home) {
		t.Fatalf("home: %q %v", d, err)
	}
	if _, err := ResolveDir(filepath.Join(base, "nope"), base); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := ResolveDir(file, base); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file: %v", err)
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		locked := filepath.Join(base, "locked")
		_ = os.Mkdir(locked, 0o000)
		defer os.Chmod(locked, 0o755)
		if _, err := ResolveDir(locked, base); err == nil || !strings.Contains(err.Error(), "cannot be read") {
			t.Fatalf("unreadable: %v", err)
		}
	}
}

func TestBindTools(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "in.txt"), []byte("INSIDE"), 0o644)
	_ = os.WriteFile(filepath.Join(other, "out.txt"), []byte("OUTSIDE"), 0o644)
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFile{})
	reg.Register(tools.Bash{})
	b := BindTools(reg, dir)
	rf, _ := b.Get("read_file")
	if r := rf.Execute(context.Background(), map[string]string{"path": "in.txt"}); !strings.Contains(r.Output, "INSIDE") {
		t.Fatalf("relative path must resolve in dir: %+v", r)
	}
	// Unlike a worktree, a plain directory does not refuse absolute paths.
	if r := rf.Execute(context.Background(), map[string]string{"path": filepath.Join(other, "out.txt")}); !strings.Contains(r.Output, "OUTSIDE") {
		t.Fatalf("absolute path must work: %+v", r)
	}
	if runtime.GOOS != "windows" {
		bash, _ := b.Get("bash")
		r := bash.Execute(context.Background(), map[string]string{"command": "cat in.txt"})
		if !strings.Contains(r.Output, "INSIDE") {
			t.Fatalf("bash must run in dir: %+v", r)
		}
	}
}
