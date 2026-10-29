//go:build !windows

package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"openuai/internal/hooks"
	"openuai/internal/llm"
	"openuai/internal/tools"
)

func cmdHook(c string) []hooks.Handler { return []hooks.Handler{{Type: "command", Command: c}} }

// batchProvider asks for two tool calls in one response, `rounds` times.
type batchProvider struct {
	mockProvider
	rounds int
	n      int
}

func (p *batchProvider) ChatWithTools(_ context.Context, _ []llm.Message, _ string, _ []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	p.n++
	if p.n <= p.rounds {
		return &llm.Response{Model: "mock"}, []llm.ToolCall{
			{ID: "a" + string(rune('0'+p.n)), Name: "bash", Arguments: map[string]string{"command": "one"}},
			{ID: "b" + string(rune('0'+p.n)), Name: "bash", Arguments: map[string]string{"command": "two"}},
		}, nil
	}
	return &llm.Response{Content: "finished", Model: "mock"}, nil, nil
}

func lastMessage(a *Agent) llm.Message { return a.messages[len(a.messages)-1] }

func TestPostToolBatchContextAndBlock(t *testing.T) {
	asked := 0
	dir := t.TempDir()
	p := &batchProvider{rounds: 1}
	a, got := newHookAgent(t, p, hooks.Config{hooks.PostToolBatch: {{Hooks: cmdHook(`jq -r '.tool_calls | length' > ` + dir + `/n; echo '{"hookSpecificOutput":{"hookEventName":"PostToolBatch","additionalContext":"BATCH68"}}'`)}}}, &asked)
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if n, _ := os.ReadFile(dir + "/n"); strings.TrimSpace(string(n)) != "2" || len(*got) != 2 {
		t.Fatalf("batch must fire once with both calls: n=%q ran=%d", n, len(*got))
	}
	if r := lastToolResult(a); !strings.Contains(r, "BATCH68") {
		t.Fatalf("batch context must be next to the results: %q", r)
	}

	// A block stops the loop before the next model call.
	p = &batchProvider{rounds: 3}
	a, got = newHookAgent(t, p, hooks.Config{hooks.PostToolBatch: {{Hooks: cmdHook(`echo 'review needed' >&2; exit 2`)}}}, &asked)
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if p.n != 1 || len(*got) != 2 || !strings.Contains(lastMessage(a).Content, "review needed") {
		t.Fatalf("calls=%d ran=%d last=%q", p.n, len(*got), lastMessage(a).Content)
	}
}

func TestContinueFalseStopsTheTurn(t *testing.T) {
	asked := 0
	p := &batchProvider{rounds: 3}
	var steps []StepResult
	a, got := newHookAgent(t, p, hooks.Config{hooks.PostToolUse: {{Hooks: cmdHook(`echo '{"continue":false,"stopReason":"Build failed, fix it"}'`)}}}, &asked)
	a.onStep = func(s StepResult) { steps = append(steps, s) }
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	// The first tool ran, the second got a "not run" result, no more model calls.
	if p.n != 1 || len(*got) != 1 || !strings.Contains(lastToolResult(a), "not run") {
		t.Fatalf("calls=%d ran=%d last=%q", p.n, len(*got), lastToolResult(a))
	}
	shown := false
	for _, s := range steps {
		if s.Type == "error" && strings.Contains(s.Content, "Build failed, fix it") {
			shown = true
		}
	}
	if !shown || !strings.Contains(lastMessage(a).Content, "Build failed, fix it") {
		t.Fatal("stopReason must be shown and kept in the conversation")
	}
}

func TestUpdatedToolOutput(t *testing.T) {
	asked := 0
	p := &scriptProvider{tool: "bash", args: map[string]string{"command": "cat secrets"}, calls: 1}
	a, _ := newHookAgent(t, p, hooks.Config{hooks.PostToolUse: {{Matcher: "bash", Hooks: cmdHook(`echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedToolOutput":"[redacted]"}}'`)}}}, &asked)
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if r := lastToolResult(a); r != "[redacted]" {
		t.Fatalf("result %q", r)
	}
}

func newPermAgent(t *testing.T, p llm.Provider, cfg hooks.Config, approve bool, asked *int) (*Agent, *[]map[string]string) {
	t.Helper()
	s := &hooks.Set{}
	s.Add(cfg, "test")
	var mu sync.Mutex
	var got []map[string]string
	reg := tools.NewRegistry()
	reg.Register(recTool{mu: &mu, args: &got})
	pm := NewPermissionManager(t.TempDir(), func(context.Context, string, string) (PermissionLevel, bool) {
		*asked++
		return PermAlways, approve
	})
	return New(Config{Provider: p, Model: "mock", Registry: reg, Permissions: pm, Hooks: func() *hooks.Set { return s }, WorkDir: t.TempDir()}), &got
}

func TestPermissionRequestHook(t *testing.T) {
	asked := 0
	p := &scriptProvider{tool: "bash", args: map[string]string{"command": "npm test"}, calls: 1}
	a, got := newPermAgent(t, p, hooks.Config{hooks.PermissionRequest: {{Matcher: "bash", Hooks: cmdHook(`echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedInput":{"command":"npm run lint"}}}}'`)}}}, false, &asked)
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if asked != 0 || len(*got) != 1 || (*got)[0]["command"] != "npm run lint" {
		t.Fatalf("auto-allow: asked=%d got=%v", asked, *got)
	}

	// A deny from the hook: no dialog, no PermissionDenied.
	dir := t.TempDir()
	p = &scriptProvider{tool: "bash", args: map[string]string{"command": "rm x"}, calls: 1}
	a, got = newPermAgent(t, p, hooks.Config{
		hooks.PermissionRequest: {{Hooks: cmdHook(`echo '{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"not on fridays"}}}'`)}},
		hooks.PermissionDenied:  {{Hooks: cmdHook("touch " + dir + "/denied")}},
	}, true, &asked)
	_ = a.Run(context.Background(), "go")
	if asked != 0 || len(*got) != 0 || !strings.Contains(lastToolResult(a), "not on fridays") {
		t.Fatalf("deny: asked=%d got=%v result=%q", asked, *got, lastToolResult(a))
	}
	if _, err := os.Stat(dir + "/denied"); err == nil {
		t.Fatal("PermissionDenied must not fire for a hook's denial")
	}
}

func TestPermissionDeniedRetry(t *testing.T) {
	asked := 0
	p := &scriptProvider{tool: "bash", args: map[string]string{"command": "deploy"}, calls: 1}
	a, got := newPermAgent(t, p, hooks.Config{hooks.PermissionDenied: {{Matcher: "bash", Hooks: cmdHook(`jq -e '.reason | length > 0' >/dev/null && echo '{"hookSpecificOutput":{"hookEventName":"PermissionDenied","retry":true}}'`)}}}, false, &asked)
	_ = a.Run(context.Background(), "go")
	if asked != 1 || len(*got) != 0 || !strings.Contains(lastToolResult(a), "may retry") {
		t.Fatalf("asked=%d got=%v result=%q", asked, *got, lastToolResult(a))
	}
}

func TestPreToolUseAskForcesThePrompt(t *testing.T) {
	asked := 0
	p := &scriptProvider{tool: "bash", args: map[string]string{"command": "ls"}, calls: 2}
	a, got := newPermAgent(t, p, hooks.Config{hooks.PreToolUse: {{Hooks: cmdHook(`echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`)}}}, true, &asked)
	a.permissions.AllowForSession("bash")
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if asked != 2 || len(*got) != 2 {
		t.Fatalf("ask must prompt even for allowed tools: asked=%d ran=%d", asked, len(*got))
	}
}

type failingProvider struct{ mockProvider }

func (*failingProvider) ChatWithTools(context.Context, []llm.Message, string, []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	return nil, nil, errors.New("API error 429: rate limit exceeded")
}

func TestStopFailureHook(t *testing.T) {
	asked := 0
	dir := t.TempDir()
	a, _ := newHookAgent(t, &failingProvider{}, hooks.Config{
		hooks.StopFailure: {{Matcher: "rate_limit", Hooks: cmdHook(`jq -r .error_details > ` + dir + `/err`)}},
		hooks.Stop:        {{Hooks: cmdHook("touch " + dir + "/stop")}},
	}, &asked)
	if err := a.Run(context.Background(), "go"); err == nil {
		t.Fatal("the API error must be returned")
	}
	if b, _ := os.ReadFile(dir + "/err"); !strings.Contains(string(b), "429") {
		t.Fatalf("StopFailure input: %q", b)
	}
	if _, err := os.Stat(dir + "/stop"); err == nil {
		t.Fatal("Stop must not fire on an API error")
	}
	if ClassifyAPIError(errors.New("overloaded_error")) != "overloaded" || ClassifyAPIError(errors.New("weird")) != "unknown" {
		t.Fatal("classification")
	}
}

func TestCompactHooks(t *testing.T) {
	dir := t.TempDir()
	mk := func(cfg hooks.Config) *Agent {
		s := &hooks.Set{}
		s.Add(cfg, "test")
		a := New(Config{Provider: &mockProvider{response: "SUMMARY68"}, Model: "mock", Registry: tools.NewRegistry(), CompactAtTokens: 1,
			Hooks: func() *hooks.Set { return s }, WorkDir: dir, AgentType: "worker"})
		for i := 0; i < 12; i++ {
			role := llm.RoleUser
			if i%2 == 1 {
				role = llm.RoleAssistant
			}
			a.messages = append(a.messages, llm.Message{Role: role, Content: strings.Repeat("x", 100)})
		}
		return a
	}
	a := mk(hooks.Config{hooks.PreCompact: {{Matcher: "auto", Hooks: cmdHook("echo keep it >&2; exit 2")}}})
	before := len(a.messages)
	a.maybeCompact(context.Background())
	if len(a.messages) != before {
		t.Fatal("PreCompact exit 2 must block the compaction")
	}
	a = mk(hooks.Config{hooks.PostCompact: {{Matcher: "auto", Hooks: cmdHook(`jq -r .compact_summary > ` + dir + `/summary`)}}})
	a.maybeCompact(context.Background())
	if len(a.messages) >= before {
		t.Fatal("compaction must run")
	}
	if b, _ := os.ReadFile(dir + "/summary"); strings.TrimSpace(string(b)) != "SUMMARY68" {
		t.Fatalf("PostCompact summary %q", b)
	}
}

func TestHookCommonInput(t *testing.T) {
	asked := 0
	dir := t.TempDir()
	p := &scriptProvider{tool: "bash", args: map[string]string{"command": "ls"}, calls: 1}
	s := &hooks.Set{}
	s.Add(hooks.Config{hooks.PreToolUse: {{Hooks: cmdHook("cat > " + dir + "/in.json")}}}, "test")
	var mu sync.Mutex
	var got []map[string]string
	reg := tools.NewRegistry()
	reg.Register(recTool{mu: &mu, args: &got})
	pm := NewPermissionManager(t.TempDir(), func(context.Context, string, string) (PermissionLevel, bool) { asked++; return PermAlways, true })
	a := New(Config{Provider: p, Model: "mock", Registry: reg, Permissions: pm, Hooks: func() *hooks.Set { return s }, WorkDir: dir,
		SessionID: "sess-68", TranscriptPath: "/tmp/t.json", Effort: "high"})
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dir + "/in.json")
	for _, want := range []string{`"session_id":"sess-68"`, `"transcript_path":"/tmp/t.json"`, `"effort":{"level":"high"}`, `"permission_mode":"default"`, `"hook_event_name":"PreToolUse"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s in %s", want, b)
		}
	}
}
