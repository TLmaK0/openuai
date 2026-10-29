//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"openuai/internal/agent"
	"openuai/internal/hooks"
	"openuai/internal/llm"
	"openuai/internal/tools"
)

// scriptProvider returns the scripted tool calls, one entry per model call.
type scriptProvider struct {
	mu     sync.Mutex
	script [][]llm.ToolCall
	n      int
}

func (p *scriptProvider) Chat(context.Context, []llm.Message, string) (*llm.Response, error) {
	return &llm.Response{Content: "x"}, nil
}
func (p *scriptProvider) Name() string     { return "script" }
func (p *scriptProvider) Models() []string { return []string{"m"} }
func (p *scriptProvider) ChatWithTools(context.Context, []llm.Message, string, []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.n
	p.n++
	if i < len(p.script) && p.script[i] != nil {
		return &llm.Response{}, p.script[i], nil
	}
	return &llm.Response{Content: "done"}, nil, nil
}

// fakeBash records the commands it runs.
type fakeBash struct{ ran *[]string }

func (b fakeBash) Definition() tools.Definition {
	return tools.Definition{Name: "bash", RequiresPermission: "none"}
}
func (b fakeBash) Execute(_ context.Context, args map[string]string) tools.Result {
	*b.ran = append(*b.ran, args["command"])
	return tools.Result{Output: "ok"}
}

func skillSources(s *hooks.Set, src string) int {
	n := 0
	for _, e := range s.Entries {
		if e.Source == src {
			n++
		}
	}
	return n
}

func TestSkillHooksRegisterOnInvokeAndPersist(t *testing.T) {
	a, work := hookApp(t, `{"hooks":{}}`)
	onceLog := filepath.Join(work, "once.log")
	writeSkill(t, work, "guard", `---
description: guard
hooks:
  PreToolUse:
    - matcher: bash
      hooks:
        - type: command
          command: "jq -r .tool_input.command | grep -q '^rm' && { echo 'rm is blocked by skill guard' >&2; exit 2; }; exit 0"
        - type: command
          once: true
          command: "echo ran >> `+onceLog+`"
---
Guard active.`)
	if a.loadHooks().Has(hooks.PreToolUse) {
		t.Fatal("skill hooks must not be active before the skill is invoked")
	}
	if _, err := a.planSkillInvocation(context.Background(), "/guard"); err != nil {
		t.Fatal(err)
	}
	if n := skillSources(a.loadHooks(), "skill:guard"); n != 2 {
		t.Fatalf("registered %d hooks, want 2", n)
	}
	// Invoking it again does not register its hooks a second time.
	if _, err := a.planSkillInvocation(context.Background(), "/guard"); err != nil {
		t.Fatal(err)
	}
	if n := skillSources(a.loadHooks(), "skill:guard"); n != 2 {
		t.Fatalf("after a second invocation: %d hooks, want 2", n)
	}

	var ran []string
	reg := tools.NewRegistry()
	reg.Register(fakeBash{ran: &ran})
	bash := func(id, cmd string) []llm.ToolCall {
		return []llm.ToolCall{{ID: id, Name: "bash", Arguments: map[string]string{"command": cmd}}}
	}
	p := &scriptProvider{script: [][]llm.ToolCall{
		bash("1", "rm -rf build"), bash("2", "echo hi"), nil, // turn 1
		bash("3", "rm notes.txt"), nil, // turn 2: the hook still applies
	}}
	ag := agent.New(agent.Config{Provider: p, Model: "m", Registry: reg, Hooks: a.loadHooks, WorkDir: work})
	if err := ag.Run(context.Background(), "turn one"); err != nil {
		t.Fatal(err)
	}
	if err := ag.Run(context.Background(), "turn two"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ran, "|") != "echo hi" {
		t.Fatalf("ran %q: the rm commands must be blocked on both turns", ran)
	}
	blocked := 0
	for _, m := range ag.Messages() {
		if m.Role == llm.RoleToolResult && strings.Contains(m.Content, "rm is blocked by skill guard") {
			blocked++
		}
	}
	if blocked != 2 {
		t.Fatalf("blocked %d calls, want 2", blocked)
	}
	data, _ := os.ReadFile(onceLog)
	if got := strings.Count(string(data), "ran"); got != 1 {
		t.Fatalf("the once hook ran %d times, want 1", got)
	}
	if n := skillSources(a.loadHooks(), "skill:guard"); n != 1 {
		t.Fatalf("the once hook must be removed after its first success: %d left", n)
	}
}

func TestSkillHooksOnceStaysAfterFailure(t *testing.T) {
	a, work := hookApp(t, `{"hooks":{}}`)
	log := filepath.Join(work, "tries.log")
	writeSkill(t, work, "flaky", `---
description: flaky
hooks:
  UserPromptSubmit:
    - hooks:
        - type: command
          once: true
          command: "echo try >> `+log+`; test $(wc -l < `+log+`) -ge 2"
---
F`)
	if _, err := a.planSkillInvocation(context.Background(), "/flaky"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		a.runHookOn(context.Background(), a.loadHooks(), hooks.UserPromptSubmit, "", map[string]any{"prompt": "x"})
	}
	data, _ := os.ReadFile(log)
	// First run fails (stays), second succeeds (removed), third does not run.
	if got := strings.Count(string(data), "try"); got != 2 {
		t.Fatalf("ran %d times, want 2", got)
	}
}

func TestSkillHooksFromModelAndForkInvocations(t *testing.T) {
	a, work := hookApp(t, `{"hooks":{}}`)
	hook := "hooks:\n  PostToolUse:\n    - matcher: read_file\n      hooks:\n        - type: command\n          command: \"true\"\n"
	writeSkill(t, work, "model-skill", "---\ndescription: m\n"+hook+"---\nM")
	writeSkill(t, work, "fork-skill", "---\ndescription: f\ncontext: fork\n"+hook+"---\nF")
	writeSkill(t, work, "bad-hooks", "---\ndescription: b\nhooks:\n  NoSuchEvent:\n    - hooks:\n        - type: command\n          command: \"true\"\n---\nB")
	if r := a.invokeSkill(context.Background(), "model-skill", ""); r.Error != "" {
		t.Fatal(r.Error)
	}
	if n := skillSources(a.loadHooks(), "skill:model-skill"); n != 1 {
		t.Fatalf("model invocation: %d hooks", n)
	}
	plan, err := a.planSkillInvocation(context.Background(), "/fork-skill topic")
	if err != nil || plan.fork == nil {
		t.Fatalf("fork plan: %+v %v", plan, err)
	}
	if n := skillSources(a.loadHooks(), "skill:fork-skill"); n != 1 {
		t.Fatalf("fork invocation: %d hooks", n)
	}
	// An invalid event is reported and skipped, the skill still loads.
	if _, err := a.planSkillInvocation(context.Background(), "/bad-hooks"); err != nil {
		t.Fatal(err)
	}
	if n := skillSources(a.loadHooks(), "skill:bad-hooks"); n != 0 {
		t.Fatalf("invalid hooks registered: %d", n)
	}
}
