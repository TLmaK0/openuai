package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openuai/internal/agent"
	"openuai/internal/llm"
)

// project makes a git repository with an AGENTS.md saying rule.
func project(t *testing.T, rule string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(rule), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func instructionPaths(a *App) []string {
	var out []string
	for _, f := range a.GetInstructionFiles() {
		out = append(out, f.Path)
	}
	return out
}

func TestSetWorkDirMovesTheConversation(t *testing.T) {
	a := mainAgentApp(t, nil)
	ag := a.ensureAgent()
	ag.Begin("hello")
	proj := project(t, "PROJECT_RULE")

	if msg := a.SetWorkDir(proj); msg != "" {
		t.Fatal(msg)
	}
	if got := a.GetWorkDir(); got != proj {
		t.Fatalf("work dir = %q, want %q", got, proj)
	}
	if got := instructionPaths(a); len(got) != 1 || got[0] != filepath.Join(proj, "AGENTS.md") {
		t.Fatalf("instruction files = %v", got)
	}
	ag2 := a.ensureAgent()
	if ag2 == ag || !hasUserMessages(ag2.Messages()) {
		t.Fatal("the agent must be rebuilt keeping the conversation")
	}
	if !strings.Contains(ag2.SystemPrompt(), "Working directory: "+proj) {
		t.Fatal("the system prompt must name the new working directory")
	}
	// Saved with the session, and remembered as a recent folder.
	if got := agent.PeekSessionWorkDir(a.cfg.ConfigDir(), "current"); got != proj {
		t.Fatalf("saved work dir = %q", got)
	}
	if got := a.GetRecentWorkDirs(); len(got) == 0 || got[0] != proj {
		t.Fatalf("recent = %v", got)
	}

	// Relative paths start at the current folder.
	if err := os.Mkdir(filepath.Join(proj, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if msg := a.SetWorkDir("sub"); msg != "" {
		t.Fatal(msg)
	}
	if got := a.GetWorkDir(); got != filepath.Join(proj, "sub") {
		t.Fatalf("relative: work dir = %q", got)
	}
}

func TestSetWorkDirRefusals(t *testing.T) {
	a := mainAgentApp(t, nil)
	start := a.GetWorkDir()
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"missing": filepath.Join(start, "nope"), "a file": file, "empty": " "} {
		if msg := a.SetWorkDir(path); msg == "" {
			t.Errorf("%s: expected an error", name)
		}
	}
	a.agentMu.Lock()
	msg := a.SetWorkDir(t.TempDir())
	a.agentMu.Unlock()
	if msg != errWorkDirBusy {
		t.Fatalf("busy: %q", msg)
	}
	if a.GetWorkDir() != start {
		t.Fatal("a refused change must leave the folder alone")
	}
}

func TestSetWorkDirExpandsHome(t *testing.T) {
	a := mainAgentApp(t, nil)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.Mkdir(filepath.Join(home, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if msg := a.SetWorkDir("~/repo"); msg != "" {
		t.Fatal(msg)
	}
	if got := a.GetWorkDir(); got != filepath.Join(home, "repo") {
		t.Fatalf("work dir = %q", got)
	}
}

func TestResumeAndStartupGoBackToTheSessionFolder(t *testing.T) {
	a := mainAgentApp(t, nil)
	a.costTracker = llm.NewCostTracker()
	proj := project(t, "PROJECT_RULE")
	if msg := a.SetWorkDir(proj); msg != "" {
		t.Fatal(msg)
	}
	a.ensureAgent().Begin("hello")
	a.saveSession(a.ensureAgent())
	id := agent.ArchiveSession(a.cfg.ConfigDir())
	if id == "" {
		t.Fatal("nothing archived")
	}
	if s := agent.ListSessions(a.cfg.ConfigDir()); len(s) != 1 || s[0].WorkDir != proj {
		t.Fatalf("sessions = %+v", s)
	}

	other := t.TempDir()
	if msg := a.SetWorkDir(other); msg != "" {
		t.Fatal(msg)
	}
	if msg := a.ResumeSession(id); msg != "" {
		t.Fatal(msg)
	}
	if got := a.GetWorkDir(); got != proj {
		t.Fatalf("after resume: %q", got)
	}

	// A launch from the home directory restores the session's folder; a
	// launch from a specific folder keeps it.
	a.saveSession(a.ensureAgent())
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, tc := range []struct {
		launch, want string
	}{{home, proj}, {other, other}} {
		b := appWith(map[string]llm.Provider{"p": inTreeProvider{name: "p"}})
		b.cfg, b.registry = a.cfg, a.registry
		if err := os.Chdir(tc.launch); err != nil {
			t.Fatal(err)
		}
		b.restoreSessionWorkDir()
		if got := b.GetWorkDir(); got != tc.want {
			t.Errorf("launched from %s: work dir = %q, want %q", tc.launch, got, tc.want)
		}
	}
}

func TestRestartUsesTheLaunchFolder(t *testing.T) {
	a := mainAgentApp(t, nil)
	a.launchDir = "/launch"
	if msg := a.SetWorkDir(t.TempDir()); msg != "" {
		t.Fatal(msg)
	}
	if got, _ := a.launchWorkDir(); got != "/launch" {
		t.Fatalf("launch dir = %q", got)
	}
}
