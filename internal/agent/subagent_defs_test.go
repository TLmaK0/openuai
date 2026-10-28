package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"openuai/internal/llm"
	"openuai/internal/tools"
)

// loopProvider never finishes: every turn calls a tool again.
type loopProvider struct {
	mockProvider
	calls int32
}

func (p *loopProvider) ChatWithTools(_ context.Context, _ []llm.Message, _ string, _ []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	n := atomic.AddInt32(&p.calls, 1)
	return &llm.Response{Content: fmt.Sprintf("progress %d", n), InputTokens: 1, OutputTokens: 1, Model: "mock"},
		[]llm.ToolCall{{ID: fmt.Sprintf("tc-%d", n), Name: "read_file", Arguments: map[string]string{}}}, nil
}

func TestSubAgentMaxTurnsReturnsPartial(t *testing.T) {
	p := &loopProvider{}
	reg := tools.NewRegistry()
	reg.Register(mockTool{name: "read_file", output: "data"})

	res := RunSubAgents(context.Background(), SubAgentConfig{
		Provider: p, Model: "mock", Registry: reg, Permissions: NewPermissionManager("", nil), MaxConcurrent: 1,
	}, []SubAgentTask{{ID: "loop", Description: "never ends", MaxTurns: 3}})

	r := res[0]
	if !r.Partial || r.Error != "" {
		t.Fatalf("expected a partial result without error, got %+v", r)
	}
	if got := atomic.LoadInt32(&p.calls); got != 3 {
		t.Fatalf("expected exactly 3 turns, got %d", got)
	}
	if r.Output != "progress 3" {
		t.Fatalf("partial output should be the work so far, got %q", r.Output)
	}
	if out := FormatSubAgentResults(res); !strings.Contains(out, tools.PartialNote) {
		t.Fatalf("formatted result must be marked partial: %q", out)
	}
}

// recordProvider remembers the model and system prompt of each call.
type recordProvider struct {
	mockProvider
	mu      sync.Mutex
	models  []string
	systems []string
}

func (p *recordProvider) Chat(_ context.Context, msgs []llm.Message, model string) (*llm.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = append(p.models, model)
	p.systems = append(p.systems, msgs[0].Content)
	return &llm.Response{Content: "ok", Model: model}, nil
}

func TestSubAgentPerTaskSettingsAndSetupError(t *testing.T) {
	p := &recordProvider{}
	res := RunSubAgents(context.Background(), SubAgentConfig{
		Provider: p, Model: "parent", Registry: tools.NewRegistry(), Permissions: NewPermissionManager("", nil), MaxConcurrent: 1,
	}, []SubAgentTask{
		{ID: "custom", Description: "x", Agent: "reviewer", Model: "special", SystemPrompt: "CUSTOM PROMPT", Notice: "fell back"},
		{ID: "broken", Description: "y", Agent: "ghost", SetupError: "unknown agent \"ghost\""},
	})

	if len(p.models) != 1 || p.models[0] != "special" || !strings.HasPrefix(p.systems[0], "CUSTOM PROMPT") {
		t.Fatalf("per-task model/prompt not used: models=%v systems=%v", p.models, p.systems)
	}
	if strings.Contains(p.systems[0], "You are OpenUAI, an autonomous") {
		t.Fatal("sub-agent must not get the main system prompt")
	}
	if res[0].Model != "special" || res[0].Agent != "reviewer" || res[0].Notice != "fell back" {
		t.Fatalf("result metadata: %+v", res[0])
	}
	if res[1].Error == "" || res[1].Output != "" {
		t.Fatalf("setup error must fail the task without running it: %+v", res[1])
	}
}
