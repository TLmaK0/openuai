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

// userTexts lists what the user said in the conversation in progress.
func userTexts(a *App) []string {
	var out []string
	for _, m := range a.ensureAgent().Messages() {
		if m.Role == llm.RoleUser {
			out = append(out, m.Content)
		}
	}
	return out
}

func sessionIDs(a *App) []string {
	var out []string
	for _, s := range agent.ListSessions(a.cfg.ConfigDir()) {
		out = append(out, s.ID+"@"+s.WorkDir)
	}
	return out
}

func TestSetWorkDirSwitchesToTheFolderSessions(t *testing.T) {
	a := mainAgentApp(t, nil)
	home := a.GetWorkDir()
	a.ensureAgent().Begin("in home")
	proj := project(t, "PROJECT_RULE")

	// A folder without sessions starts a new conversation; the one in
	// progress is archived with its folder.
	if msg := a.SetWorkDir(proj); msg != "" {
		t.Fatal(msg)
	}
	if got := a.GetWorkDir(); got != proj {
		t.Fatalf("work dir = %q, want %q", got, proj)
	}
	if got := userTexts(a); len(got) != 0 {
		t.Fatalf("new folder: conversation = %v", got)
	}
	if got := instructionPaths(a); len(got) != 1 || got[0] != filepath.Join(proj, "AGENTS.md") {
		t.Fatalf("instruction files = %v", got)
	}
	if !strings.Contains(a.ensureAgent().SystemPrompt(), "Working directory: "+proj) {
		t.Fatal("the system prompt must name the new working directory")
	}
	if got := agent.PeekSessionWorkDir(a.cfg.ConfigDir(), "current"); got != proj {
		t.Fatalf("saved work dir = %q", got)
	}
	if got := a.GetRecentWorkDirs(); len(got) == 0 || got[0] != proj {
		t.Fatalf("recent = %v", got)
	}
	a.ensureAgent().Begin("in project")

	// Going back opens the folder's last session, which leaves the archive.
	if msg := a.SetWorkDir(home); msg != "" {
		t.Fatal(msg)
	}
	if got := userTexts(a); len(got) != 1 || got[0] != "in home" {
		t.Fatalf("back home: conversation = %v", got)
	}
	if got := sessionIDs(a); len(got) != 1 || !strings.HasSuffix(got[0], "@"+proj) {
		t.Fatalf("archive = %v", got)
	}
	if msg := a.SetWorkDir(proj); msg != "" {
		t.Fatal(msg)
	}
	if got := userTexts(a); len(got) != 1 || got[0] != "in project" {
		t.Fatalf("back to the project: conversation = %v", got)
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

func TestResumeArchivesTheConversationInProgress(t *testing.T) {
	a := mainAgentApp(t, nil)
	proj := project(t, "PROJECT_RULE")
	if msg := a.SetWorkDir(proj); msg != "" {
		t.Fatal(msg)
	}
	a.ensureAgent().Begin("first")
	a.saveSession(a.ensureAgent())
	a.ClearChat()
	ids := agent.ListSessions(a.cfg.ConfigDir())
	if len(ids) != 1 {
		t.Fatalf("sessions = %v", sessionIDs(a))
	}
	other := t.TempDir()
	if msg := a.SetWorkDir(other); msg != "" {
		t.Fatal(msg)
	}
	a.ensureAgent().Begin("second")

	if msg := a.ResumeSession(ids[0].ID); msg != "" {
		t.Fatal(msg)
	}
	if got := a.GetWorkDir(); got != proj {
		t.Fatalf("after resume: %q", got)
	}
	if got := userTexts(a); len(got) != 1 || got[0] != "first" {
		t.Fatalf("resumed conversation = %v", got)
	}
	// "second" was archived, "first" left the archive: no duplicates.
	if got := sessionIDs(a); len(got) != 1 || !strings.HasSuffix(got[0], "@"+other) {
		t.Fatalf("archive = %v", got)
	}

	// A session of another model is refused and nothing changes.
	a.saveSession(a.ensureAgent())
	a.ClearChat()
	a.cfg.SetProviderAndModel("p", "m2")
	a.currentAgent = nil
	before := sessionIDs(a)
	var firstID string
	for _, s := range agent.ListSessions(a.cfg.ConfigDir()) {
		if s.WorkDir == proj {
			firstID = s.ID
		}
	}
	if msg := a.ResumeSession(firstID); !strings.Contains(msg, "switch to that provider and model") {
		t.Fatalf("mismatch: %q", msg)
	}
	if got := sessionIDs(a); strings.Join(got, ",") != strings.Join(before, ",") || a.GetWorkDir() != proj {
		t.Fatalf("a refused resume changed things: %v in %s", got, a.GetWorkDir())
	}
}

func TestStartupFolder(t *testing.T) {
	a := mainAgentApp(t, nil)
	proj := project(t, "PROJECT_RULE")
	other := t.TempDir()
	if msg := a.SetWorkDir(other); msg != "" {
		t.Fatal(msg)
	}
	a.ensureAgent().Begin("in other")
	if msg := a.SetWorkDir(proj); msg != "" {
		t.Fatal(msg)
	}
	a.ensureAgent().Begin("in project")
	a.saveSession(a.ensureAgent())

	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	newApp := func(launch string) *App {
		b := appWith(map[string]llm.Provider{"p": inTreeProvider{name: "p"}})
		b.cfg, b.registry, b.costTracker = a.cfg, a.registry, llm.NewCostTracker()
		if err := os.Chdir(launch); err != nil {
			t.Fatal(err)
		}
		b.restoreSessionWorkDir()
		return b
	}

	// Wherever it is launched from (home, another folder, the checkout of a
	// dev restart), the last conversation and its folder come back.
	for _, launch := range []string{home, other} {
		b := newApp(launch)
		if got := b.GetWorkDir(); got != proj {
			t.Errorf("launched from %s: work dir = %q", launch, got)
		}
		if got := userTexts(b); len(got) != 1 || got[0] != "in project" {
			t.Errorf("launched from %s: conversation = %v", launch, got)
		}
	}

	// A conversation whose folder is gone stays where the app was launched.
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if msg := a.SetWorkDir(gone); msg != "" {
		t.Fatal(msg)
	}
	a.ensureAgent().Begin("in gone")
	a.saveSession(a.ensureAgent())
	if err := os.Chdir(other); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if got := newApp(other).GetWorkDir(); got != other {
		t.Fatalf("folder gone: work dir = %q", got)
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
