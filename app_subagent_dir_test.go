//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"openuai/internal/agent"
	"openuai/internal/config"
	"openuai/internal/llm"
	"openuai/internal/tools"
)

// pwdProvider runs bash pwd once, then answers with the tool's output. It
// records the system prompt and the tool descriptions it was given.
type pwdProvider struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	descs   []string
}

func (p *pwdProvider) Chat(context.Context, []llm.Message, string) (*llm.Response, error) {
	return &llm.Response{Content: "x"}, nil
}
func (p *pwdProvider) Name() string     { return "mock" }
func (p *pwdProvider) Models() []string { return []string{"mock"} }
func (p *pwdProvider) ChatWithTools(_ context.Context, msgs []llm.Message, _ string, defs []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.prompts = append(p.prompts, msgs[0].Content)
	for _, d := range defs {
		p.descs = append(p.descs, d.Name+": "+d.Description)
	}
	last := msgs[len(msgs)-1]
	if last.Role == llm.RoleToolResult {
		return &llm.Response{Content: "PWD=" + strings.TrimSpace(last.Content), Model: "mock"}, nil, nil
	}
	return &llm.Response{Model: "mock"}, []llm.ToolCall{{ID: "1", Name: "bash", Arguments: map[string]string{"command": "pwd"}}}, nil
}

func writeT(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// dirApp is an App working in its own temp dir, with a SubagentStart hook
// that leaves a mark, and another project (a git repository) with its own
// AGENTS.md, agent definitions, skill and SubagentStart hook.
func dirApp(t *testing.T) (a *App, p *pwdProvider, work, proj, marks string) {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	work, _ = filepath.EvalSymlinks(t.TempDir())
	proj, _ = filepath.EvalSymlinks(t.TempDir())
	marks = t.TempDir()
	t.Chdir(work)
	hook := func(name string) string {
		return `{"hooks":{"SubagentStart":[{"hooks":[{"type":"command","command":"touch '` + filepath.Join(marks, name) + `'"}]}]}}`
	}
	writeT(t, filepath.Join(work, ".openuai", "hooks.json"), hook("app"))
	writeT(t, filepath.Join(work, "AGENTS.md"), "APPRULE")
	writeT(t, filepath.Join(proj, ".openuai", "hooks.json"), hook("proj"))
	writeT(t, filepath.Join(proj, "AGENTS.md"), "Always end answers with PROJ82")
	writeT(t, filepath.Join(proj, ".openuai", "agents", "x.md"), "---\nname: x82\ndescription: project agent\n---\nYou are X82.")
	writeT(t, filepath.Join(proj, ".openuai", "agents", "y.md"), "---\nname: y82\ndescription: isolated\nisolation: worktree\n---\nYou are Y82.")
	writeT(t, filepath.Join(proj, ".openuai", "skills", "s82", "SKILL.md"), "---\nname: s82\ndescription: SKILL82 of the project\n---\nDo it")
	gitRun(t, proj, "init", "-q", "-b", "master")
	gitRun(t, proj, "add", ".")
	gitRun(t, proj, "commit", "-q", "-m", "init")

	p = &pwdProvider{}
	reg := tools.NewRegistry()
	reg.Register(tools.Bash{})
	reg.Register(tools.ReadFile{})
	a = &App{cfg: cfg, costTracker: llm.NewCostTracker(), registry: reg,
		providers:   map[string]llm.Provider{cfg.Provider(): p},
		permissions: agent.NewPermissionManager(cfg.ConfigDir(), func(context.Context, string, string) (agent.PermissionLevel, bool) { return agent.PermSession, true }),
	}
	reg.Register(tools.Skill{Listing: a.skillListing, Invoke: a.invokeSkill})
	return a, p, work, proj, marks
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestSubAgentInAnotherDir(t *testing.T) {
	a, p, work, proj, marks := dirApp(t)
	res := a.spawnSubAgents(context.Background(), []tools.SubTask{{ID: "t1", Agent: "x82", Cwd: proj, Description: "where are you?"}})
	if res[0].Error != "" {
		t.Fatalf("error: %s", res[0].Error)
	}
	if res[0].Output != "PWD="+proj {
		t.Fatalf("bash must run in the project dir, got %q", res[0].Output)
	}
	sys := p.prompts[0]
	for _, want := range []string{"You are X82.", "PROJ82", "## Working directory\n" + proj, "Current branch: master"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "APPRULE") {
		t.Fatal("the app's AGENTS.md must not be loaded")
	}
	if !strings.Contains(strings.Join(p.descs, "\n"), "SKILL82") {
		t.Fatal("the skill tool must list the project's skills")
	}
	if !exists(filepath.Join(marks, "proj")) || exists(filepath.Join(marks, "app")) {
		t.Fatalf("the project's hook must run instead of the app's (proj=%v app=%v)", exists(filepath.Join(marks, "proj")), exists(filepath.Join(marks, "app")))
	}

	// Relative cwd is taken from the delegating agent's directory.
	rel, _ := filepath.Rel(work, proj)
	res = a.spawnSubAgents(context.Background(), []tools.SubTask{{ID: "t2", Cwd: rel, Description: "pwd"}})
	if res[0].Output != "PWD="+proj {
		t.Fatalf("relative cwd: %+v", res[0])
	}
}

func TestSubAgentWithoutDirUnchanged(t *testing.T) {
	a, p, work, _, marks := dirApp(t)
	res := a.spawnSubAgents(context.Background(), []tools.SubTask{{ID: "t1", Agent: "x82", Description: "x"}})
	if !strings.Contains(res[0].Error, "unknown agent") {
		t.Fatalf("the project agent must not exist in the app's dir: %+v", res[0])
	}
	res = a.spawnSubAgents(context.Background(), []tools.SubTask{{ID: "t2", Description: "x"}})
	if res[0].Output != "PWD="+work || !strings.Contains(p.prompts[0], "APPRULE") {
		t.Fatalf("without cwd the sub-agent works in the app's dir: %+v", res[0])
	}
	if !exists(filepath.Join(marks, "app")) {
		t.Fatal("the app's hook must run without cwd")
	}
}

func TestSubAgentBadDir(t *testing.T) {
	a, p, work, _, _ := dirApp(t)
	file := filepath.Join(work, "AGENTS.md")
	res := a.spawnSubAgents(context.Background(), []tools.SubTask{
		{ID: "missing", Cwd: "/nonexistent/i82", Description: "x"},
		{ID: "file", Cwd: file, Description: "x"},
	})
	if !strings.Contains(res[0].Error, "/nonexistent/i82 does not exist") || !strings.Contains(res[1].Error, "is not a directory") {
		t.Fatalf("errors: %q / %q", res[0].Error, res[1].Error)
	}
	if p.calls != 0 {
		t.Fatal("nothing must run for a bad directory")
	}
}

func TestSubAgentInAnotherDirIsolated(t *testing.T) {
	a, p, _, proj, _ := dirApp(t)
	res := a.spawnSubAgents(context.Background(), []tools.SubTask{{ID: "iso", Agent: "y82", Cwd: proj, Description: "pwd"}})
	if res[0].Error != "" {
		t.Fatal(res[0].Error)
	}
	out := strings.TrimPrefix(res[0].Output, "PWD=")
	if out == proj || !strings.Contains(out, "openuai-agent-") {
		t.Fatalf("must run in a worktree, got %q", out)
	}
	if !strings.Contains(p.prompts[0], "Do not touch the main checkout at "+proj) {
		t.Fatalf("the worktree must come from the project's repository:\n%s", p.prompts[0])
	}
	if !strings.Contains(res[0].Notice, "removed") {
		t.Fatalf("clean worktree must be removed: %q", res[0].Notice)
	}
}

func TestNestedSubAgentInheritsDir(t *testing.T) {
	a, _, work, proj, _ := dirApp(t)
	ctx := withSubAgentScope(context.Background(), subAgentScope{depth: 1, key: "r/p", dir: proj})
	if d := a.dirOf(ctx); d != proj {
		t.Fatalf("dirOf = %q", d)
	}
	if d := a.dirOf(context.Background()); d != work {
		t.Fatalf("main dirOf = %q", d)
	}
	res := a.spawnSubAgents(ctx, []tools.SubTask{{ID: "child", Agent: "x82", Description: "pwd"}})
	if res[0].Error != "" || res[0].Output != "PWD="+proj {
		t.Fatalf("nested sub-agent must inherit the dir: %+v", res[0])
	}
}
