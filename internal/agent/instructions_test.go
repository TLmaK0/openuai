package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"openuai/internal/tools"
)

// The instruction files are read at the start of every turn: an edit applies
// to the next message, and an unchanged file keeps the system message
// byte-identical (so session-keyed providers keep their session).
func TestInstructionsRefreshEveryTurn(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	rules := "## Instructions (AGENTS.md)\n\nend with RULE_ALPHA"
	calls := 0
	ag := New(Config{
		Provider: &promptRecorder{seen: &seen, mu: &mu},
		Model:    "mock",
		Registry: tools.NewRegistry(),
		Instructions: func() string {
			calls++
			return rules
		},
	})
	base := ag.SystemPrompt()
	for _, msg := range []string{"a", "b"} {
		if err := ag.Run(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	rules = "## Instructions (AGENTS.md)\n\nend with RULE_BETA"
	if err := ag.Run(context.Background(), "c"); err != nil {
		t.Fatal(err)
	}
	rules = ""
	if err := ag.Run(context.Background(), "d"); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || len(seen) != 4 {
		t.Fatalf("calls=%d prompts=%d", calls, len(seen))
	}
	if seen[0] != seen[1] || !strings.HasSuffix(seen[0], "end with RULE_ALPHA") || !strings.HasPrefix(seen[0], base) {
		t.Fatalf("unchanged instructions must give the same prompt: %q / %q", seen[0], seen[1])
	}
	if !strings.HasSuffix(seen[2], "end with RULE_BETA") || strings.Contains(seen[2], "RULE_ALPHA") {
		t.Fatalf("edited instructions: %q", seen[2])
	}
	if seen[3] != base {
		t.Fatalf("without instructions the prompt is the default one: %q", seen[3])
	}
}

func TestNoInstructionsCallbackKeepsPrompt(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	ag := New(Config{Provider: &promptRecorder{seen: &seen, mu: &mu}, Model: "mock", Registry: tools.NewRegistry()})
	base := ag.SystemPrompt()
	if err := ag.Run(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != base {
		t.Fatalf("prompt changed: %q", seen)
	}
}
