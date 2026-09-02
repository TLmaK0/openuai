package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"openuai/internal/llm"
)

// The end-to-end tests below run a real plugin in a real child process. The
// child is this same test binary, re-executed with pluginEnv set, which keeps
// the test honest — actual pipes, actual JSON framing, actual process
// lifetime — without needing a second binary built for each platform.
const pluginEnv = "OPENUAI_TEST_PROVIDER_PLUGIN"

func TestMain(m *testing.M) {
	if os.Getenv(pluginEnv) != "" {
		runTestPlugin()
		return
	}
	os.Exit(m.Run())
}

// testPluginDialer speaks to this test binary acting as a plugin. mode is
// passed through so a test can ask for a misbehaving one.
func testPluginDialer(mode string) Dialer {
	return StdioDialer(os.Args[0], nil, map[string]string{pluginEnv: mode})
}

// runTestPlugin is the child process: a newline-delimited JSON-RPC server on
// stdin/stdout, which is all a provider plugin has to be.
func runTestPlugin() {
	mode := os.Getenv(pluginEnv)
	if mode == "silent" {
		// A plugin that starts and then answers nothing at all.
		select {}
	}

	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	// Credentials live in the child, which is the point of the mechanism.
	var secret string

	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}

		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			return
		}

		reply := func(result any) {
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": req.ID, "result": result,
			})
			out.Write(body)
			out.WriteString("\n")
			out.Flush()
		}
		replyError := func(message string) {
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": message},
			})
			out.Write(body)
			out.WriteString("\n")
			out.Flush()
		}

		switch req.Method {
		case MethodDescribe:
			if mode == "longline" {
				// One line well over the drain's per-line cap, written before
				// any ordinary logging. A drain that gives up on an over-long
				// line stops reading here, and the pipe fills on what comes
				// next.
				os.Stderr.Write(bytes.Repeat([]byte("y"), 8*1024))
				os.Stderr.Write([]byte("\n"))
			}
			if mode == "chatty" || mode == "longline" {
				// Far more than any pipe buffer (64 KiB on Linux, less on
				// Windows). A plugin whose stderr nobody reads blocks here in
				// write(2) and never gets to answer.
				noise := make([]byte, 1024)
				for i := range noise {
					noise[i] = 'x'
				}
				for written := 0; written < 1200*1024; written += len(noise) + 1 {
					os.Stderr.Write(noise)
					os.Stderr.Write([]byte("\n"))
				}
			}
			if mode == "nonsense" {
				// A plugin writing rubbish to stdout must not be trusted.
				out.WriteString("this is not JSON-RPC\n")
				out.Flush()
				continue
			}
			reply(Description{
				Name:                "e2e",
				DisplayName:         "End To End (API key)",
				Credential:          string(llm.CredentialSecret),
				SecretPlaceholder:   "e2e-...",
				DefaultModel:        "e2e-large",
				Models:              []string{"e2e-large"},
				Pricing:             map[string][2]float64{"e2e-large": {1.0, 2.0}},
				SupportsTools:       true,
				SupportsReady:       true,
				SupportsSecret:      true,
				SupportsFetchModels: true,
			})

		case MethodChat:
			var call ChatRequest
			json.Unmarshal(req.Params, &call)
			text := ""
			if len(call.Messages) > 0 {
				text = call.Messages[len(call.Messages)-1].Content
			}
			reply(ChatResult{Response: &llm.Response{
				Content:      "echo: " + text,
				Model:        call.Model,
				InputTokens:  len(call.Messages),
				OutputTokens: 7,
			}})

		case MethodChatWithTools:
			var call ChatRequest
			json.Unmarshal(req.Params, &call)
			if len(call.Tools) == 0 {
				replyError("no tools offered")
				continue
			}
			reply(ChatResult{
				Response: &llm.Response{Model: call.Model, OutputTokens: 3},
				ToolCalls: []llm.ToolCall{{
					ID:        "call-1",
					Name:      call.Tools[0].Name,
					Arguments: map[string]string{"echoed": call.Tools[0].Description},
				}},
			})

		case MethodReady:
			reply(ReadyResult{Ready: secret != ""})

		case MethodSetSecret:
			var call SecretRequest
			json.Unmarshal(req.Params, &call)
			secret = call.Secret
			reply(struct{}{})

		case MethodFetchModels:
			if secret == "" {
				replyError("not authenticated")
				continue
			}
			reply(ModelsResult{Models: []string{"e2e-large", "e2e-live"}})

		default:
			replyError("unknown method: " + req.Method)
		}
	}
}

// A provider that lives in another process is discovered, registered and used
// exactly like a compiled-in one — no rebuild of the core involved.
func TestEndToEndOverARealProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	desc, err := Describe(ctx, testPluginDialer("normal"))
	if err != nil {
		t.Fatalf("Describe() = %v, want nil", err)
	}
	if desc.Name != "e2e" || desc.DisplayName != "End To End (API key)" {
		t.Fatalf("Describe() = %+v", desc)
	}
	if desc.Pricing["e2e-large"] != [2]float64{1.0, 2.0} {
		t.Errorf("Describe().Pricing = %v", desc.Pricing)
	}

	client := NewClient(desc, testPluginDialer("normal"))
	defer client.Close()

	// The descriptor a plugin yields satisfies the same contract the agent
	// loop uses, tool calls included.
	descriptor := desc.Descriptor(func(llm.Store) llm.Provider { return client.Provider() })
	built := descriptor.New(nil)
	if _, ok := built.(llm.ToolCallProvider); !ok {
		t.Fatal("a plugin provider does not implement ToolCallProvider")
	}

	// Not ready until it has its credentials, which it keeps itself.
	if llm.Ready(client) {
		t.Error("Ready() before any secret = true, want false")
	}
	if err := llm.SetSecret(client, "e2e-secret"); err != nil {
		t.Fatalf("SetSecret() = %v, want nil", err)
	}
	if !llm.Ready(client) {
		t.Error("Ready() after the secret = false, want true")
	}

	resp, err := client.Chat(ctx, []llm.Message{{Role: llm.RoleUser, Content: "ping"}}, "e2e-large")
	if err != nil {
		t.Fatalf("Chat() = %v, want nil", err)
	}
	if resp.Content != "echo: ping" || resp.Model != "e2e-large" || resp.OutputTokens != 7 {
		t.Errorf("Chat() = %+v", resp)
	}

	tools, ok := client.Provider().(llm.ToolCallProvider)
	if !ok {
		t.Fatal("the plugin declared tool support but is not a ToolCallProvider")
	}
	resp, calls, err := tools.ChatWithTools(ctx,
		[]llm.Message{{Role: llm.RoleUser, Content: "use a tool"}},
		"e2e-large",
		[]llm.ToolDefinition{{Name: "bash", Description: "run a command"}})
	if err != nil {
		t.Fatalf("ChatWithTools() = %v, want nil", err)
	}
	if len(calls) != 1 || calls[0].Name != "bash" || calls[0].Arguments["echoed"] != "run a command" {
		t.Errorf("tool calls = %+v", calls)
	}
	if resp.OutputTokens != 3 {
		t.Errorf("ChatWithTools().OutputTokens = %d, want 3", resp.OutputTokens)
	}

	// The live model list is served over the same channel.
	models := llm.AvailableModels(ctx, client)
	if len(models) != 2 || models[1] != "e2e-live" {
		t.Errorf("AvailableModels() = %v, want the live list", models)
	}

	// Its usage is costed like any other provider's, from the prices it
	// declared in its own description.
	for model, price := range desc.Pricing {
		llm.SetModelPricing(model, price[0], price[1])
	}
	entry := llm.NewCostTracker().Track(&llm.Response{
		Model: "e2e-large", InputTokens: 1_000_000, OutputTokens: 1_000_000,
	})
	if entry.CostUSD != 3.0 {
		t.Errorf("cost = %v, want 3 (1 in + 2 out per million)", entry.CostUSD)
	}
}

// A plugin that answers with something other than JSON-RPC must be refused,
// not half-trusted.
func TestPluginWritingRubbishIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if _, err := Describe(ctx, testPluginDialer("nonsense")); err == nil {
		t.Error("Describe() on a plugin writing rubbish = nil, want an error")
	}
}

// The settings screen asks every provider whether it is ready. A plugin that
// never answers must not hold that up indefinitely, so the readiness check
// carries its own deadline rather than relying on a caller to supply one.
func TestReadinessOfASilentPluginIsBounded(t *testing.T) {
	client := NewClient(Description{Name: "silent", SupportsReady: true}, testPluginDialer("silent"))
	defer client.Close()

	start := time.Now()
	if client.Ready() {
		t.Error("Ready() on a silent plugin = true, want false")
	}
	elapsed := time.Since(start)
	if elapsed < readyTimeout {
		t.Errorf("Ready() gave up after %v, before its own deadline", elapsed)
	}
	if elapsed > readyTimeout+10*time.Second {
		t.Errorf("Ready() took %v, want it bounded by its deadline", elapsed)
	}
}

// A plugin that never answers must time out with the caller's context rather
// than hanging the agent loop for good.
func TestUnresponsivePluginRespectsTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	_, err := Describe(ctx, testPluginDialer("silent"))
	if err == nil {
		t.Fatal("Describe() on a silent plugin = nil, want a deadline error")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("Describe() took %v, want it to give up with the context", elapsed)
	}
}

// A missing executable is a configuration mistake, and has to be reported as
// one instead of crashing the core.
func TestMissingExecutableIsReported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dial := StdioDialer(fmt.Sprintf("openuai-no-such-plugin-%d", os.Getpid()), nil, nil)
	if _, err := Describe(ctx, dial); err == nil {
		t.Error("Describe() on a missing executable = nil, want an error")
	}
}

// The transport opens the child's stderr pipe but never reads it, so a plugin
// that logs blocks in write(2) as soon as the buffer fills and stops
// answering. Measured before the drain: ~31 KiB was fine, ~1.2 MiB never
// answered at all.
func TestAChattyPluginIsNotDeadlockedByItsOwnStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	desc, err := Describe(ctx, testPluginDialer("chatty"))
	if err != nil {
		t.Fatalf("Describe() on a plugin that logs 1.2 MiB to stderr = %v, want it to answer", err)
	}
	if desc.Name != "e2e" {
		t.Errorf("Describe() = %+v", desc)
	}
	t.Logf("answered after %v", time.Since(start))
}

// The per-line cap on the drain must bound what is logged, not what is read.
// A single line over it used to end the drain — bufio.Scanner stops on a
// token larger than its buffer — so the plugin blocked in write(2) on the
// next thing it logged. Measured at the head that introduced the cap: 1.2 MiB
// of short lines answered immediately, one 8 KiB line alone answered, and one
// 8 KiB line followed by ordinary logging never answered at all.
func TestALongStderrLineDoesNotStopTheDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	desc, err := Describe(ctx, testPluginDialer("longline"))
	if err != nil {
		t.Fatalf("Describe() on a plugin logging an 8 KiB line and then 1.2 MiB = %v, want it to answer", err)
	}
	if desc.Name != "e2e" {
		t.Errorf("Describe() = %+v", desc)
	}
	t.Logf("answered after %v", time.Since(start))
}

// The bound itself still holds: what is over it is reported as dropped rather
// than logged, so a plugin writing without newlines cannot use the log as its
// buffer.
func TestALongStderrLineIsLoggedUpToTheBound(t *testing.T) {
	long := strings.Repeat("y", maxStderrLine+500)
	reader := bufio.NewReader(strings.NewReader(long + "\nshort\n"))

	line, err := readBoundedLine(reader)
	if err != nil {
		t.Fatalf("readBoundedLine() = %v, want nil", err)
	}
	if !strings.HasPrefix(line, strings.Repeat("y", maxStderrLine)) {
		t.Errorf("the first %d bytes of the line were not kept", maxStderrLine)
	}
	if !strings.Contains(line, "500 more bytes") {
		t.Errorf("readBoundedLine() = %q, want the dropped bytes reported", line[maxStderrLine:])
	}

	// And the reader is left on the next line, not abandoned.
	line, err = readBoundedLine(reader)
	if err != nil {
		t.Fatalf("readBoundedLine() after a long line = %v, want nil", err)
	}
	if line != "short" {
		t.Errorf("readBoundedLine() after a long line = %q, want short", line)
	}
}
