package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"openuai/internal/llm"
	"openuai/internal/tools"
)

// silentLoopProvider only calls tools and never writes any text.
type silentLoopProvider struct {
	mockProvider
	calls int32
}

func (p *silentLoopProvider) ChatWithTools(_ context.Context, _ []llm.Message, _ string, _ []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	n := atomic.AddInt32(&p.calls, 1)
	return &llm.Response{InputTokens: 1, OutputTokens: 1, Model: "mock"},
		[]llm.ToolCall{{ID: fmt.Sprintf("tc-%d", n), Name: "list_dir", Arguments: map[string]string{"path": fmt.Sprintf("/dir%d", n)}}}, nil
}

// A sub-agent stopped at maxTurns before writing any text must still report
// the tool work it did, never an empty result.
func TestSubAgentMaxTurnsPartialWithoutText(t *testing.T) {
	p := &silentLoopProvider{}
	reg := tools.NewRegistry()
	reg.Register(mockTool{name: "list_dir", output: "a.txt\nb.txt"})

	res := RunSubAgents(context.Background(), SubAgentConfig{
		Provider: p, Model: "mock", Registry: reg, Permissions: NewPermissionManager("", nil), MaxConcurrent: 1,
	}, []SubAgentTask{{ID: "silent", Description: "list dirs", MaxTurns: 1}})

	r := res[0]
	if !r.Partial || r.Error != "" {
		t.Fatalf("expected partial without error, got %+v", r)
	}
	if !strings.Contains(r.Output, "list_dir") || !strings.Contains(r.Output, "/dir1") || !strings.Contains(r.Output, "a.txt") {
		t.Fatalf("partial output must describe the tool work, got %q", r.Output)
	}
}
