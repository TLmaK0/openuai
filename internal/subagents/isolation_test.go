package subagents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"openuai/internal/tools"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "master")
	_ = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("A"), 0o644)
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "init")
	return repo
}

func TestParseIsolation(t *testing.T) {
	d, err := Parse([]byte("---\nname: a\ndescription: x\nisolation: Worktree\n---\nbody"))
	if err != nil || d.Isolation != IsolationWorktree {
		t.Fatalf("got %+v %v", d, err)
	}
	if _, err := Parse([]byte("---\nname: a\ndescription: x\nisolation: docker\n---\nbody")); err == nil {
		t.Fatal("invalid isolation must be rejected")
	}
}

func TestWorktreeRemovedWithoutChanges(t *testing.T) {
	repo := newRepo(t)
	w, err := CreateWorktree(repo, "my task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Dir, "a.txt")); err != nil {
		t.Fatal("worktree must contain the repository")
	}
	if kept, _ := w.Finish(); kept {
		t.Fatal("clean worktree must be removed")
	}
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		t.Fatal("worktree dir still there")
	}
	if strings.Contains(gitT(t, repo, "branch"), w.Branch) {
		t.Fatal("branch must be deleted")
	}
}

func TestIsolatedToolsAndKeptWorktree(t *testing.T) {
	repo := newRepo(t)
	w, err := CreateWorktree(repo, "edit")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { gitT(t, repo, "worktree", "remove", "--force", w.Dir) }()
	reg := tools.NewRegistry()
	reg.Register(tools.WriteFile{})
	reg.Register(tools.Bash{})
	iso := IsolateTools(reg, w)

	wf, _ := iso.Get("write_file")
	if r := wf.Execute(context.Background(), map[string]string{"path": "b.txt", "content": "B"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	if _, err := os.Stat(filepath.Join(w.Dir, "b.txt")); err != nil {
		t.Fatal("relative path must resolve inside the worktree")
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err == nil {
		t.Fatal("main checkout must not be touched")
	}
	if r := wf.Execute(context.Background(), map[string]string{"path": filepath.Join(repo, "c.txt"), "content": "C"}); r.Error == "" {
		t.Fatal("absolute path into the main checkout must be refused")
	}
	b, _ := iso.Get("bash")
	if r := b.Execute(context.Background(), map[string]string{"command": "pwd"}); !strings.Contains(r.Output, w.Dir) {
		t.Fatalf("bash must run in the worktree, got %q", r.Output)
	}
	if kept, note := w.Finish(); !kept || !strings.Contains(note, w.Branch) {
		t.Fatalf("worktree with changes must be kept: %v %s", kept, note)
	}
}
