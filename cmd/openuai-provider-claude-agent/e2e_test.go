package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"openuai/internal/llm"
	"openuai/internal/llm/plugin"
)

// The tests below run the real invocation against a real child process, so the
// pipes, the argument list and the stream parsing are exercised together rather
// than in isolation. The child is this test binary re-executed, which keeps it
// honest without needing a second binary built per platform — and without
// needing the real agent installed, which a CI machine has no reason to have.
const fakeEnv = "OPENUAI_TEST_FAKE_CLAUDE"

// argSep joins the arguments the fake was given so a test can split them back
// out exactly, empty values included.
const argSep = "<|>"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		runFakeClaude(mode)
		return
	}
	os.Exit(m.Run())
}

// runFakeClaude stands in for the headless agent. In its default mode it
// echoes the arguments it was given into the result, so a test asserts on what
// actually reached the child rather than on what the caller believed it passed.
func runFakeClaude(mode string) {
	line := func(v any) {
		body, _ := json.Marshal(v)
		fmt.Println(string(body))
	}
	init := map[string]any{"type": "system", "subtype": "init", "model": "claude-opus-5", "tools": []string{}}

	switch mode {
	case "authfail":
		line(init)
		line(map[string]any{"type": "system", "subtype": "api_retry", "attempt": 1,
			"max_retries": 10, "error_status": 401, "error": "authentication_failed"})
		// It would go on retrying for about three minutes; the plugin must not
		// wait for that.
	case "badflag":
		fmt.Fprintln(os.Stderr, "Error: unknown option '--tools'")
		os.Exit(1)
	case "tools":
		line(init)
		line(map[string]any{"type": "result",
			"structured_output": map[string]any{"reply": "", "tool_calls": []map[string]any{
				{"name": "bash", "arguments": map[string]string{"command": "ls"}}}},
			"usage":          map[string]int{"input_tokens": 9, "output_tokens": 4},
			"total_cost_usd": 0.002})
	default:
		line(init)
		line(map[string]any{"type": "result",
			"result":         strings.Join(os.Args[1:], argSep),
			"usage":          map[string]int{"input_tokens": 12, "output_tokens": 5},
			"total_cost_usd": 0.003})
	}
	os.Exit(0)
}

// withFake points the plugin at this test binary running as the fake agent.
func withFake(t *testing.T, mode string) {
	t.Helper()
	t.Setenv(fakeEnv, mode)
	old := binary
	binary = os.Args[0]
	t.Cleanup(func() { binary = old })
}

// A whole run through a real process: the flags that make this a model backend
// have to arrive at the child, not merely be assembled.
func TestRunPassesTheBackendFlagsToARealProcess(t *testing.T) {
	withFake(t, "echo")

	out, err := invocation{model: "opus", system: "sys", prompt: "hello"}.run(context.Background())
	if err != nil {
		t.Fatalf("run() = %v", err)
	}

	got := strings.Split(out.Text, argSep)
	for _, want := range []string{"-p", "hello", "--output-format", "stream-json", "--verbose",
		"--restricted", "--strict-mcp-config", "--model", "opus", "--system-prompt"} {
		if !contains(got, want) {
			t.Errorf("the child did not receive %q; it got %q", want, got)
		}
	}
	// --tools followed by an empty value is the flag that leaves the agent no
	// tools of its own, and an empty value is exactly what a looser check for
	// the flag alone would miss.
	if !followedByEmpty(got, "--tools") {
		t.Errorf("the child did not receive --tools with an empty value: %q", got)
	}
	if contains(got, "--bare") {
		t.Error("a prior-login run reached the child with --bare, which reads no OAuth credentials")
	}

	if out.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want the resolved id", out.Model)
	}
	if out.InputTokens != 12 || out.OutputTokens != 5 {
		t.Errorf("tokens = %d/%d, want 12/5", out.InputTokens, out.OutputTokens)
	}
	if out.CostUSD == 0 {
		t.Error("the run reported no cost, so nothing could be charged against it")
	}
}

// The API key path is the one --bare belongs to, and the key must not travel
// as an argument where a process listing would show it.
func TestApiKeyRunUsesBareAndKeepsTheKeyOutOfTheArguments(t *testing.T) {
	withFake(t, "echo")

	out, err := invocation{prompt: "hello", apiKey: "sk-ant-secret-value"}.run(context.Background())
	if err != nil {
		t.Fatalf("run() = %v", err)
	}
	got := strings.Split(out.Text, argSep)
	if !contains(got, "--bare") {
		t.Errorf("an API key run reached the child without --bare: %q", got)
	}
	if strings.Contains(out.Text, "sk-ant-secret-value") {
		t.Error("the API key was passed as an argument, where any process listing would show it")
	}
}

// An authentication failure has to come back from a real run promptly and by
// name, instead of the call being abandoned on the core's deadline while the
// child retries.
func TestRunReportsAuthenticationFailureFromARealProcess(t *testing.T) {
	withFake(t, "authfail")

	if _, err := (invocation{prompt: "hello"}).run(context.Background()); err == nil {
		t.Fatal("run() = nil error on an authentication failure")
	} else if !strings.Contains(err.Error(), "auth login") {
		t.Errorf("error = %q, want it to name what fixes it", err)
	}
}

// A flag the installed version does not know is the outdated-version case. The
// exit code alone says nothing, so the child's own message is what surfaces.
func TestRunSurfacesTheChildsErrorMessage(t *testing.T) {
	withFake(t, "badflag")

	if _, err := (invocation{prompt: "hello"}).run(context.Background()); err == nil {
		t.Fatal("run() = nil error when the child exited non-zero")
	} else if !strings.Contains(err.Error(), "unknown option") {
		t.Errorf("error = %q, want the child's own message", err)
	}
}

// A missing binary is the third failure state, and it must be named as such
// rather than reported as a failed turn.
func TestRunReportsAMissingBinary(t *testing.T) {
	old := binary
	binary = "openuai-no-such-agent-binary"
	t.Cleanup(func() { binary = old })

	_, err := invocation{prompt: "hello"}.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v, want it to say the binary is not installed", err)
	}
}

// The tool path end to end: openuai's tools go out in the prompt and come back
// as tool calls for openuai's own loop to run.
func TestChatWithToolsReturnsToolCallsThroughARealProcess(t *testing.T) {
	withFake(t, "tools")

	result, err := chatWithTools(context.Background(), "sk-ant-test", plugin.ChatRequest{
		Model:    "opus",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "list the files"}},
		Tools: []llm.ToolDefinition{{
			Name:        "bash",
			Description: "run a command",
			Parameters:  []llm.ToolParam{{Name: "command", Type: "string", Description: "the command", Required: true}},
		}},
	})
	if err != nil {
		t.Fatalf("chatWithTools() = %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Name != "bash" || result.ToolCalls[0].Arguments["command"] != "ls" {
		t.Errorf("tool call = %+v", result.ToolCalls[0])
	}
	if result.Response == nil || result.Response.Model != "claude-opus-5" {
		t.Errorf("response = %+v, want the resolved model", result.Response)
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func followedByEmpty(args []string, flag string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == "" {
			return true
		}
	}
	return false
}
