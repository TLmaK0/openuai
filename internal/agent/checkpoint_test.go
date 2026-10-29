package agent

import (
	"context"
	"testing"

	"openuai/internal/tools"
)

// Every tool call and tool result is checkpointed during the turn, so a
// restart mid-turn keeps the work done so far.
func TestCheckpointDuringTurn(t *testing.T) {
	p := &silentLoopProvider{}
	reg := tools.NewRegistry()
	reg.Register(mockTool{name: "list_dir", output: "x"})
	var sizes []int
	a := New(Config{Provider: p, Model: "mock", Registry: reg, Permissions: NewPermissionManager("", nil), MaxTurns: 2,
		OnCheckpoint: func(ag *Agent) { sizes = append(sizes, len(ag.Messages())) }})
	_ = a.Run(context.Background(), "go")
	if len(sizes) != 4 {
		t.Fatalf("expected 4 checkpoints (call+result per turn), got %v", sizes)
	}
	for i := 1; i < len(sizes); i++ {
		if sizes[i] <= sizes[i-1] {
			t.Fatalf("history must grow at each checkpoint: %v", sizes)
		}
	}
}
