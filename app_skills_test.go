package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openuai/internal/agent"
	"openuai/internal/llm"
	"openuai/internal/tools"
)

func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, ".openuai", "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// inProject runs the test with a temporary project as the working directory.
func inProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return dir
}

func TestPlanSkillInvocationStacksSkills(t *testing.T) {
	dir := inProject(t)
	writeSkill(t, dir, "tests", "---\ndescription: write tests\n---\nWrite tests for issue $0.")
	writeSkill(t, dir, "fix", "---\ndescription: fix\nallowed-tools: Bash(go test *)\nmodel: inherit\n---\nFix issue $ARGUMENTS.")
	writeSkill(t, dir, "forky", "---\ndescription: f\ncontext: fork\n---\nResearch $ARGUMENTS")
	writeSkill(t, dir, "hidden", "---\ndescription: h\nuser-invocable: false\n---\nH")
	a := NewApp()

	plan, err := a.planSkillInvocation(context.Background(), "/tests /fix 123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.content, "Write tests for issue 123.") || !strings.Contains(plan.content, "Fix issue 123.") || len(plan.changes) != 2 {
		t.Fatalf("stacked: %+v", plan)
	}
	if agent.DisplayContent(llmUser(plan.content)) != "/tests /fix 123" {
		t.Fatalf("the user sees what they typed: %q", agent.DisplayContent(llmUser(plan.content)))
	}
	if c := plan.changes[1]; c.Allow == nil || !c.Allow("bash", "go test ./...") || c.Allow("bash", "rm x") || c.Model != "" {
		t.Fatalf("turn change: %+v", c)
	}

	// A forked skill ends the stack: it and the rest become the arguments.
	plan, _ = a.planSkillInvocation(context.Background(), "/tests /forky topic")
	if len(plan.changes) != 1 || !strings.Contains(plan.content, "Write tests for issue /forky.") || plan.fork != nil {
		t.Fatalf("fork in a stack: %+v", plan)
	}
	// A forked skill first runs alone.
	plan, _ = a.planSkillInvocation(context.Background(), "/forky topic")
	if plan.fork == nil || plan.fork.Name != "forky" || !strings.Contains(plan.forkRendered, "Research topic") || len(plan.changes) != 0 {
		t.Fatalf("fork: %+v", plan)
	}
	// user-invocable: false is not run by /name.
	plan, _ = a.planSkillInvocation(context.Background(), "/hidden x")
	if plan.content != "/hidden x" {
		t.Fatalf("hidden: %q", plan.content)
	}
}

func TestPlanSkillInjectionNeedsPermission(t *testing.T) {
	dir := inProject(t)
	writeSkill(t, dir, "denied", "---\ndescription: d\n---\nNow: !`echo INJECTED`")
	writeSkill(t, dir, "allowed", "---\ndescription: a\nallowed-tools: Bash(echo *)\n---\nNow: !`echo INJECTED-$0`")
	a := NewApp()
	if _, err := a.planSkillInvocation(context.Background(), "/denied"); err == nil || !strings.Contains(err.Error(), "permission check failed") {
		t.Fatalf("err = %v", err)
	}
	plan, err := a.planSkillInvocation(context.Background(), "/allowed ok")
	if err != nil || !strings.Contains(plan.content, "Now: INJECTED-ok") {
		t.Fatalf("err=%v content=%q", err, plan.content)
	}
}

func TestInvokeSkillRules(t *testing.T) {
	dir := inProject(t)
	writeSkill(t, dir, "deploy", "---\ndescription: deploy\ndisable-model-invocation: true\n---\nDEPLOY")
	writeSkill(t, dir, "style", "---\ndescription: style\neffort: low\n---\nSTYLE ${OPENUAI_EFFORT}")
	a := NewApp()
	if r := a.invokeSkill(context.Background(), "deploy", ""); r.Error == "" {
		t.Fatal("disable-model-invocation must block the assistant")
	}
	// Naming it as /deploy in the message gives permission for that message.
	if _, err := a.planSkillInvocation(context.Background(), "go ahead and /deploy to staging"); err != nil {
		t.Fatal(err)
	}
	if r := a.invokeSkill(context.Background(), "deploy", ""); r.Error != "" || !strings.Contains(r.Output, "DEPLOY") {
		t.Fatalf("permitted: %+v", r)
	}
	_, _ = a.planSkillInvocation(context.Background(), "next message")
	if r := a.invokeSkill(context.Background(), "deploy", ""); r.Error == "" {
		t.Fatal("the permission lasts one message")
	}

	// The turn gets the skill's settings; identical content is not added twice.
	var applied []tools.TurnChange
	var history string
	turn := &tools.Turn{
		Has:    func(s string) bool { return strings.Contains(history, s) },
		Apply:  func(c tools.TurnChange) { applied = append(applied, c) },
		Effort: "medium",
	}
	ctx := tools.WithTurn(context.Background(), turn)
	r := a.invokeSkill(ctx, "style", "")
	if !strings.Contains(r.Output, "STYLE medium") || len(applied) != 1 || applied[0].Effort != "low" {
		t.Fatalf("r=%+v applied=%+v", r, applied)
	}
	history = r.Output
	if r := a.invokeSkill(ctx, "style", ""); !strings.Contains(r.Output, "already loaded") || len(applied) != 2 {
		t.Fatalf("dedup: %+v", r)
	}
}

func TestNoteSkillPathsActivatesPathAndNestedSkills(t *testing.T) {
	dir := inProject(t)
	writeSkill(t, dir, "go-style", "---\ndescription: Go style\npaths: \"**/*.go\"\n---\nG")
	web := filepath.Join(dir, "apps", "web")
	writeSkill(t, web, "web-deploy", "---\ndescription: deploy web\n---\nW")
	a := NewApp()
	if l := a.skillListing(); strings.Contains(l, "go-style") || strings.Contains(l, "web-deploy") {
		t.Fatalf("not active yet:\n%s", l)
	}
	if n := a.noteSkillPaths("read_file", map[string]string{"path": "README.md"}); n != "" {
		t.Fatalf("no match: %q", n)
	}
	note := a.noteSkillPaths("read_file", map[string]string{"path": filepath.Join(dir, "cmd", "main.go")})
	if !strings.Contains(note, "go-style") {
		t.Fatalf("note: %q", note)
	}
	if n := a.noteSkillPaths("read_file", map[string]string{"path": "cmd/main.go"}); n != "" {
		t.Fatalf("announced twice: %q", n)
	}
	note = a.noteSkillPaths("write_file", map[string]string{"path": "apps/web/src/index.ts"})
	if !strings.Contains(note, "web-deploy") {
		t.Fatalf("nested: %q", note)
	}
	if l := a.skillListing(); !strings.Contains(l, "go-style") || !strings.Contains(l, "web-deploy") {
		t.Fatalf("listing:\n%s", l)
	}
	if n := a.noteSkillPaths("bash", map[string]string{"command": "cat x.go"}); n != "" {
		t.Fatal("only file tools count")
	}
	a.skills.reset()
	if l := a.skillListing(); strings.Contains(l, "go-style") {
		t.Fatal("a new chat starts over")
	}
}

func TestForkAgentName(t *testing.T) {
	inProject(t)
	a := NewApp()
	if n := a.forkAgentName("Explore"); n != "explore" {
		t.Fatalf("%q", n)
	}
	if n := a.forkAgentName(""); n != "general-purpose" {
		t.Fatalf("%q", n)
	}
}

func TestSkillStateForks(t *testing.T) {
	var s skillState
	if !s.forkStart("x") || s.forkStart("x") {
		t.Fatal("a second background run of the same skill must wait")
	}
	s.forkDone("x")
	if !s.forkStart("x") {
		t.Fatal("free again")
	}
}

func llmUser(c string) llm.Message { return llm.Message{Role: llm.RoleUser, Content: c} }
