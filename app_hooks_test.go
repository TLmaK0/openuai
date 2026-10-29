//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openuai/internal/agent"
	"openuai/internal/config"
	"openuai/internal/llm"
	"openuai/internal/tools"
)

// hookApp is an App with an isolated config dir and working directory, and
// the given user hooks.json.
func hookApp(t *testing.T, hooksJSON string) (*App, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	t.Chdir(work)
	if err := os.WriteFile(filepath.Join(cfg.ConfigDir(), "hooks.json"), []byte(hooksJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return &App{cfg: cfg, costTracker: llm.NewCostTracker()}, work
}

func waitFile(t *testing.T, p string) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s was not written", p)
	return ""
}

func TestUserPromptExpansionHook(t *testing.T) {
	a, work := hookApp(t, `{"hooks":{"UserPromptExpansion":[
		{"matcher":"deploy","hooks":[{"type":"command","command":"jq -r '.command_args + \"|\" + .expansion_type' >&2; exit 2"}]},
		{"matcher":"review","hooks":[{"type":"command","command":"echo '{\"hookSpecificOutput\":{\"hookEventName\":\"UserPromptExpansion\",\"additionalContext\":\"CHECKLIST68\"}}'"}]}]}}`)
	for _, n := range []string{"deploy", "review"} {
		d := filepath.Join(work, ".openuai", "skills", n)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+n+"\ndescription: test\n---\nDo "+n), 0o644)
	}
	if _, err := a.planSkillInvocation(context.Background(), "/deploy staging"); err == nil || !strings.Contains(err.Error(), "staging|slash_command") {
		t.Fatalf("deploy must be blocked with the hook's stderr: %v", err)
	}
	plan, err := a.planSkillInvocation(context.Background(), "/review now")
	if err != nil || !strings.Contains(plan.content, "Do review") || len(plan.notes) != 1 || plan.notes[0] != "CHECKLIST68" {
		t.Fatalf("review: %q %v %v", plan.content, plan.notes, err)
	}
}

func TestMessageDisplayHook(t *testing.T) {
	a, _ := hookApp(t, `{"hooks":{"MessageDisplay":[{"hooks":[{"type":"command","command":"jq '{hookSpecificOutput: {hookEventName: \"MessageDisplay\", displayContent: (.delta | gsub(\"\\\\*\\\\*\"; \"\"))}}'"}]}]}}`)
	got := a.displayStep(agent.StepResult{Type: "done", Content: "**bold** text"})
	if got.Content != "bold text" {
		t.Fatalf("display: %q", got.Content)
	}
	if s := a.displayStep(agent.StepResult{Type: "tool_result", Content: "**x**"}); s.Content != "**x**" {
		t.Fatal("only assistant text is displayed through the hook")
	}
}

func TestModelSwitchHooks(t *testing.T) {
	a, _ := hookApp(t, `{"hooks":{
		"PreModelSwitch":[{"matcher":".*retired.*","hooks":[{"type":"command","command":"echo 'model retired' >&2; exit 2"}]}],
		"PostModelSwitch":[{"hooks":[{"type":"command","command":"jq -r '\"now on \" + .to_model + \" from \" + .from_model'"}]}]}}`)
	if err := a.preModelSwitch("m1", "old-retired-model", "old-retired-model", "picker"); err == nil || !strings.Contains(err.Error(), "model retired") {
		t.Fatalf("pre: %v", err)
	}
	if err := a.preModelSwitch("m1", "m2", "m2", "picker"); err != nil {
		t.Fatal(err)
	}
	a.postModelSwitch("m1", "m2", "m2", "picker")
	if n := a.takeHookNotes(); len(n) != 1 || n[0] != "now on m2 from m1" {
		t.Fatalf("post: %v", n)
	}
}

func TestSessionStartAndEnd(t *testing.T) {
	a, work := hookApp(t, `{"hooks":{
		"SessionStart":[{"matcher":"startup|clear","hooks":[{"type":"command","command":"echo '{\"hookSpecificOutput\":{\"hookEventName\":\"SessionStart\",\"additionalContext\":\"BRANCH68\",\"sessionTitle\":\"auth-refactor\"}}'"}]}],
		"SessionEnd":[{"hooks":[{"type":"command","command":"jq -r .reason > end.txt"}]}]}}`)
	a.startSession("startup", true)
	a.waitSessionStart(t.Context())
	if n := a.takeHookNotes(); len(n) != 1 || n[0] != "BRANCH68" {
		t.Fatalf("notes %v", n)
	}
	ag := agent.New(agent.Config{Provider: inTreeProvider{name: "x"}, Model: "m", Registry: tools.NewRegistry()})
	a.applyHookTitle(ag)
	if ag.Title() != "auth-refactor" {
		t.Fatalf("title %q", ag.Title())
	}
	id := a.sessionID()
	a.endSession("clear")
	if b, _ := os.ReadFile(filepath.Join(work, "end.txt")); strings.TrimSpace(string(b)) != "clear" {
		t.Fatalf("SessionEnd reason %q", b)
	}
	if a.sessionID() == id {
		t.Fatal("a new session gets a new id")
	}
}

func TestWatcherEvents(t *testing.T) {
	logf := filepath.Join(t.TempDir(), "events.log")
	a, work := hookApp(t, `{"hooks":{
		"FileChanged":[{"matcher":".envrc","hooks":[{"type":"command","command":"jq -r '\"file \" + .event' >> `+logf+`"}]}],
		"CwdChanged":[{"hooks":[{"type":"command","command":"echo cwd >> `+logf+`"}]}],
		"ConfigChange":[{"hooks":[{"type":"command","command":"jq -r '\"config \" + .source' >> `+logf+`"}]}]}}`)
	a.hk.cwd, a.hk.configSig, a.hk.skillsSig = a.GetWorkDir(), a.configSig(), a.skillsSig()
	a.checkHookChanges() // first look: nothing fires
	_ = os.WriteFile(filepath.Join(work, ".envrc"), []byte("x"), 0o644)
	_ = a.cfg.Save()
	a.checkHookChanges()
	t.Chdir(t.TempDir())
	a.checkHookChanges()
	b, _ := os.ReadFile(logf)
	for _, want := range []string{"file add", "config user_settings", "cwd"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %q in %q", want, b)
		}
	}
}

func TestNotificationHook(t *testing.T) {
	out := filepath.Join(t.TempDir(), "n.txt")
	a, _ := hookApp(t, `{"hooks":{"Notification":[{"matcher":"permission_prompt","hooks":[{"type":"command","command":"jq -r '.notification_type + \"|\" + .title' > `+out+`"}]}]}}`)
	a.notifyHook("idle_prompt", "x", "y")
	a.notifyHook("permission_prompt", "Permission needed", "bash")
	if got := strings.TrimSpace(waitFile(t, out)); got != "permission_prompt|Permission needed" {
		t.Fatalf("got %q", got)
	}
}
