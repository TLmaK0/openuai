package subagents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"openuai/internal/hooks"
	"openuai/internal/tools"
)

// IsolationWorktree is the only isolation value: the sub-agent runs in its
// own temporary git worktree (same name as Claude Code).
const IsolationWorktree = "worktree"

// Worktree is the temporary git worktree of one isolated sub-agent run.
type Worktree struct {
	Repo   string // top level of the checkout the sub-agent was launched from
	Dir    string // the worktree the sub-agent works in
	Branch string // its own branch
	Base   string // commit it was branched from (default branch)
	parent string // temporary directory holding Dir

	// Set when a WorktreeCreate hook made the worktree instead of git.
	byHook  bool
	hooks   *hooks.Set
	input   map[string]any
	workDir string
}

func runGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

var branchUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// CreateWorktree creates a worktree of the repository containing workDir,
// on a new branch from the default branch (origin's default, then main or
// master, then HEAD), in a temporary directory outside the repository.
func CreateWorktree(workDir, label string) (*Worktree, error) {
	repo, err := runGit(workDir, "rev-parse", "--show-toplevel")
	if err != nil || repo == "" {
		return nil, errors.New("isolation: worktree needs the working directory to be a git repository")
	}
	base := ""
	for _, ref := range []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master", "HEAD"} {
		if sha, err := runGit(repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil && sha != "" {
			base = sha
			break
		}
	}
	if base == "" {
		return nil, errors.New("isolation: the repository has no commit to branch the worktree from")
	}
	parent, err := os.MkdirTemp("", "openuai-agent-")
	if err != nil {
		return nil, fmt.Errorf("isolation: %w", err)
	}
	name := strings.Trim(branchUnsafe.ReplaceAllString(label, "-"), "-")
	if name == "" {
		name = "task"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	branch := "openuai/agent-" + name + "-" + hex.EncodeToString(b)
	dir := filepath.Join(parent, "repo")
	if _, err := runGit(repo, "worktree", "add", "-b", branch, dir, base); err != nil {
		_ = os.RemoveAll(parent)
		return nil, fmt.Errorf("isolation: %w", err)
	}
	return &Worktree{Repo: repo, Dir: dir, Branch: branch, Base: base, parent: parent}, nil
}

// CreateWorktreeWithHooks is CreateWorktree, except that WorktreeCreate
// hooks, when configured, replace git: they get the worktree name and return
// the directory they created. input holds the common hook input fields.
func CreateWorktreeWithHooks(workDir, label string, hs *hooks.Set, input map[string]any) (*Worktree, error) {
	if !hs.Has(hooks.WorktreeCreate) {
		return CreateWorktree(workDir, label)
	}
	name := strings.Trim(branchUnsafe.ReplaceAllString(label, "-"), "-")
	if name == "" {
		name = "task"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	name += "-" + hex.EncodeToString(b)
	in := map[string]any{}
	for k, v := range input {
		in[k] = v
	}
	in["name"] = name
	r := hs.Run(context.Background(), hooks.WorktreeCreate, "", in, workDir)
	if r.Decision == "block" {
		return nil, fmt.Errorf("isolation: the WorktreeCreate hook failed: %s", r.Reason)
	}
	if r.WorktreePath == "" {
		return nil, errors.New("isolation: the WorktreeCreate hook returned no path")
	}
	dir := r.WorktreePath
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(workDir, dir)
	}
	dir = filepath.Clean(dir)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("isolation: the WorktreeCreate hook returned %s, which is not a directory", dir)
	}
	repo, _ := runGit(workDir, "rev-parse", "--show-toplevel")
	if within(dir, repo) && dir != repo {
		realRepo, _ := filepath.EvalSymlinks(repo)
		if real, err := filepath.EvalSymlinks(dir); err != nil || !within(real, realRepo) {
			return nil, fmt.Errorf("isolation: the worktree path %s passes through a symlink inside the repository", dir)
		}
	}
	if dir == repo {
		return nil, errors.New("isolation: the WorktreeCreate hook returned the repository itself")
	}
	return &Worktree{Repo: repo, Dir: dir, byHook: true, hooks: hs, input: input, workDir: workDir}, nil
}

// Finish removes the worktree and its branch when the sub-agent changed
// nothing; otherwise it keeps them for review. It returns a note for the
// result.
//
// A worktree made by a WorktreeCreate hook is kept, as in Claude Code,
// unless WorktreeRemove hooks are configured: then they run when git sees
// no changes in it (a non-zero exit keeps it if the directory still exists).
func (w *Worktree) Finish() (kept bool, note string) {
	if w.byHook {
		return w.finishHook()
	}
	status, err1 := runGit(w.Dir, "status", "--porcelain")
	head, err2 := runGit(w.Dir, "rev-parse", "HEAD")
	if err1 == nil && err2 == nil && status == "" && head == w.Base {
		_, _ = runGit(w.Repo, "worktree", "remove", "--force", w.Dir)
		_, _ = runGit(w.Repo, "branch", "-D", w.Branch)
		_ = os.RemoveAll(w.parent)
		_, _ = runGit(w.Repo, "worktree", "prune")
		return false, "Its isolated worktree had no changes and was removed."
	}
	return true, fmt.Sprintf("Its changes are kept in the isolated worktree %s (branch %s). Review or merge them from there; remove it with: git worktree remove %s && git branch -D %s", w.Dir, w.Branch, w.Dir, w.Branch)
}

func (w *Worktree) finishHook() (bool, string) {
	if !w.hooks.Has(hooks.WorktreeRemove) {
		return true, fmt.Sprintf("Its worktree, created by a WorktreeCreate hook, is kept at %s.", w.Dir)
	}
	if status, err := runGit(w.Dir, "status", "--porcelain"); err == nil && status != "" {
		return true, fmt.Sprintf("Its changes are kept in the worktree %s (created by a WorktreeCreate hook).", w.Dir)
	}
	in := map[string]any{}
	for k, v := range w.input {
		in[k] = v
	}
	in["worktree_path"] = w.Dir
	r := w.hooks.Run(context.Background(), hooks.WorktreeRemove, "", in, w.workDir)
	if _, err := os.Stat(w.Dir); err == nil && r.Decision == "block" {
		return true, fmt.Sprintf("The WorktreeRemove hook failed; the worktree is kept at %s.", w.Dir)
	}
	return false, "Its worktree had no changes and was removed by the WorktreeRemove hook."
}

// IsolationPrompt is the system-prompt section for an isolated sub-agent.
func IsolationPrompt(w *Worktree) string {
	if w.byHook {
		return "\n\n## Isolated worktree\nYou run in your own working copy: " + w.Dir + " (created by a WorktreeCreate hook). " +
			"Your bash, git and file tools work there and relative paths resolve inside it. Do not touch the main checkout at " + w.workDir + "."
	}
	return "\n\n## Isolated worktree\nYou run in your own git worktree: " + w.Dir + " (branch " + w.Branch +
		", created from the default branch). Your bash, git and file tools work there and relative paths resolve inside it. " +
		"Do not touch the main checkout at " + w.Repo + ". Commit your changes on your branch: the worktree is kept for review when it has changes and removed when it has none."
}

var isolatedPathArgs = map[string][]string{
	"read_file": {"path"}, "write_file": {"path"}, "list_dir": {"path"}, "delete_file": {"path"},
	"move_file": {"source", "destination"}, "search_files": {"path"}, "proc_start": {"cwd"},
}

// Tools whose empty path means "the current directory".
var isolatedDirDefault = map[string]bool{"list_dir": true, "search_files": true, "proc_start": true}

// IsolateTools returns reg with every tool bound to the worktree: commands
// and git run inside it, relative paths resolve inside it, and absolute paths
// into the main checkout are refused.
func IsolateTools(reg *tools.Registry, w *Worktree) *tools.Registry {
	out := tools.NewRegistry()
	for _, t := range reg.All() {
		switch n := t.Definition().Name; n {
		case "bash":
			b, _ := t.(tools.Bash)
			out.Register(tools.Bash{WorkDir: w.Dir, Procs: b.Procs})
		case "bash_sudo":
			out.Register(tools.BashSudo{WorkDir: w.Dir})
		case "git_status":
			out.Register(tools.GitStatus{WorkDir: w.Dir})
		case "git_diff":
			out.Register(tools.GitDiff{WorkDir: w.Dir})
		case "git_log":
			out.Register(tools.GitLog{WorkDir: w.Dir})
		case "git_add":
			out.Register(tools.GitAdd{WorkDir: w.Dir})
		case "git_commit":
			out.Register(tools.GitCommit{WorkDir: w.Dir})
		case "git_branch":
			out.Register(tools.GitBranch{WorkDir: w.Dir})
		default:
			if keys, ok := isolatedPathArgs[n]; ok {
				out.Register(isolatedTool{Tool: t, w: w, keys: keys, dirDefault: isolatedDirDefault[n]})
			} else {
				out.Register(t)
			}
		}
	}
	return out
}

type isolatedTool struct {
	tools.Tool
	w          *Worktree
	keys       []string
	dirDefault bool
}

func (t isolatedTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	a := make(map[string]string, len(args))
	for k, v := range args {
		a[k] = v
	}
	for _, k := range t.keys {
		v := strings.TrimSpace(a[k])
		switch {
		case v == "":
			if t.dirDefault {
				a[k] = t.w.Dir
			}
		case strings.HasPrefix(v, "~"):
		case !filepath.IsAbs(v):
			a[k] = filepath.Join(t.w.Dir, v)
		case within(v, t.w.Repo) && !within(v, t.w.Dir):
			return tools.Result{Error: fmt.Sprintf("this sub-agent is isolated in a worktree: use paths inside %s, not the main checkout %s", t.w.Dir, t.w.Repo)}
		}
	}
	return t.Tool.Execute(ctx, a)
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
