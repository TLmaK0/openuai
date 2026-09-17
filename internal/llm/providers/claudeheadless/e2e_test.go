package claudeheadless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"openuai/internal/llm"
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

// stdinMark prefixes the prompt the fake read from stdin, so a test can tell
// it apart from the arguments echoed before it.
const stdinMark = "stdin:"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		runFakeClaude(mode)
		return
	}
	os.Exit(m.Run())
}

// runFakeClaude stands in for the headless agent. In its default mode it
// echoes the arguments it was given, and then the prompt it read from stdin,
// into the result, so a test asserts on what actually reached the child rather
// than on what the caller believed it passed.
func runFakeClaude(mode string) {
	line := func(v any) {
		body, _ := json.Marshal(v)
		fmt.Println(string(body))
	}
	init := map[string]any{"type": "system", "subtype": "init", "model": "claude-opus-5", "tools": []string{}}

	switch mode {
	case "models", "models-empty", "models-error", "models-malformed", "models-hang":
		var request struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			os.Exit(2)
		}
		if request.Type != "control_request" || request.Request.Subtype != "initialize" {
			os.Exit(3)
		}
		args := strings.Join(os.Args[1:], argSep)
		for _, want := range []string{"--input-format" + argSep + "stream-json", "--tools" + argSep + argSep, "--strict-mcp-config", `{"disableAllHooks":true}`, "--no-session-persistence"} {
			if !strings.Contains(args, want) {
				os.Exit(4)
			}
		}
		if mode == "models-hang" {
			time.Sleep(30 * time.Second)
			return
		}
		if mode == "models-malformed" {
			fmt.Println("{")
			return
		}
		values := []map[string]string{{"value": "default"}, {"value": "opus[1m]"}, {"value": "new-model"}, {"value": "new-model"}, {"value": ""}}
		if mode == "models-empty" {
			values = nil
		}
		subtype := "success"
		if mode == "models-error" {
			subtype = "error"
		}
		line(init)
		line(map[string]any{"type": "control_response", "response": map[string]any{"request_id": "unrelated", "subtype": "success"}})
		line(map[string]any{"type": "control_response", "response": map[string]any{"request_id": request.RequestID, "subtype": subtype, "response": map[string]any{"models": values}}})
		// The catalog caller must stop the process after receiving its response.
		// It must never send a user message to discover models.
		rest, _ := io.ReadAll(os.Stdin)
		if len(rest) != 0 {
			os.Exit(5)
		}
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
		// The prompt no longer travels in argv, so the fake has to drain stdin
		// to see it at all — and draining it is also what proves the parent
		// closed the pipe instead of leaving the child waiting for more.
		prompt, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(6)
		}
		line(init)
		line(map[string]any{"type": "result",
			"result":         strings.Join(os.Args[1:], argSep) + argSep + stdinMark + string(prompt),
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

	got, stdin := splitEcho(out.Text)
	for _, want := range []string{"-p", "--output-format", "stream-json", "--verbose",
		"--restricted", "--strict-mcp-config", "--model", "opus", "--system-prompt"} {
		if !contains(got, want) {
			t.Errorf("the child did not receive %q; it got %q", want, got)
		}
	}
	// The prompt belongs on stdin, not in argv: as an argument it is capped at
	// 128 KiB, which a conversation of any length passes.
	if stdin != "hello" {
		t.Errorf("the child read %q from stdin, want the prompt", stdin)
	}
	if contains(got, "hello") {
		t.Errorf("the prompt was passed as an argument: %q", got)
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
	got, _ := splitEcho(out.Text)
	if !contains(got, "--bare") {
		t.Errorf("an API key run reached the child without --bare: %q", got)
	}
	if strings.Contains(out.Text, "sk-ant-secret-value") {
		t.Error("the API key was passed as an argument, where any process listing would show it")
	}
}

// The bug this guards: Linux caps one argument at 128 KiB (MAX_ARG_STRLEN),
// and the prompt carries the whole conversation, so once a chat grew past that
// every turn died in execve with "argument list too long" — before the agent
// ran at all. A prompt comfortably over the cap has to reach the child intact.
func TestPromptLargerThanTheArgumentLimitStillReaches(t *testing.T) {
	withFake(t, "echo")

	// 512 KiB: four times the per-argument cap, and past the 64 KiB a pipe
	// holds, so the write has to be draining while the child is being read.
	prompt := strings.Repeat("conversation line that keeps going\n", 512*1024/35)
	if len(prompt) <= 128*1024 {
		t.Fatalf("the prompt is %d bytes, which is not past the limit being tested", len(prompt))
	}

	out, err := invocation{model: "opus", prompt: prompt}.run(context.Background())
	if err != nil {
		t.Fatalf("run() = %v, want a prompt past the argument limit to go through", err)
	}
	_, stdin := splitEcho(out.Text)
	if stdin != prompt {
		t.Errorf("the child read %d bytes from stdin, want the whole %d-byte prompt", len(stdin), len(prompt))
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

	p := New(stubStore{value: "sk-ant-test"})
	resp, calls, err := p.ChatWithTools(context.Background(),
		[]llm.Message{{Role: llm.RoleUser, Content: "list the files"}},
		"opus",
		[]llm.ToolDefinition{{
			Name:        "bash",
			Description: "run a command",
			Parameters:  []llm.ToolParam{{Name: "command", Type: "string", Description: "the command", Required: true}},
		}})
	if err != nil {
		t.Fatalf("ChatWithTools() = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if calls[0].Name != "bash" || calls[0].Arguments["command"] != "ls" {
		t.Errorf("tool call = %+v", calls[0])
	}
	if resp == nil || resp.Model != "claude-opus-5" {
		t.Errorf("response = %+v, want the resolved model", resp)
	}
}

// splitEcho takes the echo fake's result apart into the arguments the child
// was given and the prompt it read from stdin.
func splitEcho(result string) ([]string, string) {
	args := strings.Split(result, argSep)
	stdin := ""
	if n := len(args) - 1; n >= 0 && strings.HasPrefix(args[n], stdinMark) {
		stdin = strings.TrimPrefix(args[n], stdinMark)
		args = args[:n]
	}
	return args, stdin
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
