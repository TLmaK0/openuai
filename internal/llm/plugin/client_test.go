package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"openuai/internal/llm"
)

// fakeConn answers requests from a table, without a child process.
type fakeConn struct {
	results map[string]any
	// failWith, when set, makes every SendRequest fail, standing in for a
	// plugin that died.
	failWith error
	// rpcError, when set, is returned as a JSON-RPC error for every call.
	rpcError string
	// raw overrides the encoded result of a method with arbitrary bytes.
	raw map[string]string

	started int
	closed  int
	calls   []string
	params  map[string]json.RawMessage
}

func newFakeConn(results map[string]any) *fakeConn {
	return &fakeConn{results: results, raw: map[string]string{}, params: map[string]json.RawMessage{}}
}

func (f *fakeConn) Start(context.Context) error { f.started++; return nil }
func (f *fakeConn) Close() error                { f.closed++; return nil }

func (f *fakeConn) SendRequest(_ context.Context, req transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	f.calls = append(f.calls, req.Method)
	if req.Params != nil {
		encoded, err := json.Marshal(req.Params)
		if err != nil {
			return nil, err
		}
		f.params[req.Method] = encoded
	}

	if f.failWith != nil {
		return nil, f.failWith
	}
	if f.rpcError != "" {
		return &transport.JSONRPCResponse{
			JSONRPC: "2.0", ID: req.ID,
			Error: &mcp.JSONRPCErrorDetails{Message: f.rpcError},
		}, nil
	}

	var result json.RawMessage
	if override, ok := f.raw[req.Method]; ok {
		result = json.RawMessage(override)
	} else if value, ok := f.results[req.Method]; ok {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		result = encoded
	}
	return &transport.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result}, nil
}

// fullDescription describes a plugin that supports everything.
func fullDescription() Description {
	return Description{
		Name:                "acme",
		DisplayName:         "Acme (API key)",
		Credential:          "secret",
		SecretPlaceholder:   "acme-...",
		DefaultModel:        "acme-large",
		Models:              []string{"acme-large", "acme-small"},
		SupportsTools:       true,
		SupportsReady:       true,
		SupportsSecret:      true,
		SupportsLogin:       true,
		SupportsFetchModels: true,
	}
}

func dialerFor(conn Conn) Dialer { return func() Conn { return conn } }

// A description becomes a registry descriptor, which is how a plugin reaches
// the provider list and the settings screen without the core knowing it.
func TestDescriptionToDescriptor(t *testing.T) {
	d := fullDescription().Descriptor(func(llm.Store) llm.Provider { return nil })

	if d.Name != "acme" || d.DisplayName != "Acme (API key)" {
		t.Errorf("descriptor names = %q/%q", d.Name, d.DisplayName)
	}
	if d.Credential != llm.CredentialSecret {
		t.Errorf("Credential = %q, want secret", d.Credential)
	}
	if d.SecretPlaceholder != "acme-..." || d.DefaultModel != "acme-large" {
		t.Errorf("descriptor = %+v", d)
	}
	if d.Default {
		t.Error("a plugin must not claim to be the default provider")
	}
}

func TestDescriptorFallsBackOnIncompleteDescription(t *testing.T) {
	// No display name: the identifier is better than a blank dropdown entry.
	d := Description{Name: "bare"}.Descriptor(func(llm.Store) llm.Provider { return nil })
	if d.DisplayName != "bare" {
		t.Errorf("DisplayName = %q, want the name", d.DisplayName)
	}
	// An unrecognised credential kind must still draw a widget.
	if d.Credential != llm.CredentialSecret {
		t.Errorf("Credential = %q, want secret as the fallback", d.Credential)
	}
}

func TestDescribe(t *testing.T) {
	conn := newFakeConn(map[string]any{MethodDescribe: fullDescription()})

	desc, err := Describe(context.Background(), dialerFor(conn))
	if err != nil {
		t.Fatalf("Describe() = %v, want nil", err)
	}
	if desc.Name != "acme" || !desc.SupportsTools {
		t.Errorf("Describe() = %+v", desc)
	}
	if len(conn.calls) != 1 || conn.calls[0] != MethodDescribe {
		t.Errorf("calls = %v, want one describe", conn.calls)
	}
	// The probe does not leave a process behind.
	if conn.closed != 1 {
		t.Errorf("probe closed the connection %d times, want 1", conn.closed)
	}
}

// A plugin that will not say who it is cannot be registered under a name the
// core invented for it.
func TestDescribeRejectsANamelessPlugin(t *testing.T) {
	conn := newFakeConn(map[string]any{MethodDescribe: Description{DisplayName: "no name"}})
	if _, err := Describe(context.Background(), dialerFor(conn)); err == nil {
		t.Error("Describe() = nil error, want a refusal")
	}
}

func TestChatCrossesTheBoundaryIntact(t *testing.T) {
	conn := newFakeConn(map[string]any{
		MethodChat: ChatResult{Response: &llm.Response{
			Content: "hello", InputTokens: 11, OutputTokens: 22, Model: "acme-large",
		}},
	})
	client := NewClient(fullDescription(), dialerFor(conn))

	resp, err := client.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	}, "acme-large")
	if err != nil {
		t.Fatalf("Chat() = %v, want nil", err)
	}
	if resp.Content != "hello" || resp.InputTokens != 11 || resp.OutputTokens != 22 {
		t.Errorf("Chat() = %+v", resp)
	}
	// The token counts are what the cost tracker bills on, so they must
	// survive the trip.
	if resp.Model != "acme-large" {
		t.Errorf("Chat().Model = %q", resp.Model)
	}

	var sent ChatRequest
	if err := json.Unmarshal(conn.params[MethodChat], &sent); err != nil {
		t.Fatalf("request was not readable: %v", err)
	}
	if len(sent.Messages) != 1 || sent.Messages[0].Content != "hi" || sent.Model != "acme-large" {
		t.Errorf("request sent = %+v", sent)
	}
}

// Native tool calls are what the agent loop actually relies on, so the round
// trip has to preserve the definitions going out and the calls coming back.
func TestChatWithToolsRoundTrip(t *testing.T) {
	conn := newFakeConn(map[string]any{
		MethodChatWithTools: ChatResult{
			Response:  &llm.Response{Content: "", Model: "acme-large", OutputTokens: 5},
			ToolCalls: []llm.ToolCall{{ID: "tc-1", Name: "read_file", Arguments: map[string]string{"path": "go.mod"}}},
		},
	})
	client := NewClient(fullDescription(), dialerFor(conn))

	resp, calls, err := client.ChatWithTools(context.Background(),
		[]llm.Message{{Role: llm.RoleUser, Content: "read it"}},
		"acme-large",
		[]llm.ToolDefinition{{
			Name:        "read_file",
			Description: "read a file",
			Parameters:  []llm.ToolParam{{Name: "path", Type: "string", Required: true}},
		}})
	if err != nil {
		t.Fatalf("ChatWithTools() = %v, want nil", err)
	}
	if resp == nil || len(calls) != 1 {
		t.Fatalf("ChatWithTools() = %+v, %d calls", resp, len(calls))
	}
	if calls[0].ID != "tc-1" || calls[0].Name != "read_file" || calls[0].Arguments["path"] != "go.mod" {
		t.Errorf("tool call = %+v", calls[0])
	}

	var sent ChatRequest
	if err := json.Unmarshal(conn.params[MethodChatWithTools], &sent); err != nil {
		t.Fatalf("request was not readable: %v", err)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Name != "read_file" {
		t.Fatalf("tool definitions sent = %+v", sent.Tools)
	}
	if len(sent.Tools[0].Parameters) != 1 || !sent.Tools[0].Parameters[0].Required {
		t.Errorf("tool parameters lost detail: %+v", sent.Tools[0].Parameters)
	}
}

// A plugin is only asked for what it said it supports, and is never called for
// the rest.
func TestUnsupportedCapabilitiesAreNotCalled(t *testing.T) {
	conn := newFakeConn(map[string]any{})
	client := NewClient(Description{Name: "plain"}, dialerFor(conn))

	if _, _, err := client.ChatWithTools(context.Background(), nil, "m", nil); err == nil {
		t.Error("ChatWithTools() on a plugin without tool support = nil, want an error")
	}
	if err := client.SetSecret("s"); err == nil {
		t.Error("SetSecret() on a plugin taking no secret = nil, want an error")
	}
	if err := client.Login(); err == nil {
		t.Error("Login() on a plugin without login = nil, want an error")
	}
	if _, err := client.FetchModels(context.Background()); err == nil {
		t.Error("FetchModels() on a plugin that cannot fetch = nil, want an error")
	}
	// A plugin that does not report readiness is assumed ready, as in-tree.
	if !client.Ready() {
		t.Error("Ready() without SupportsReady = false, want true")
	}
	if len(conn.calls) != 0 {
		t.Errorf("calls = %v, want none", conn.calls)
	}
}

func TestReady(t *testing.T) {
	conn := newFakeConn(map[string]any{MethodReady: ReadyResult{Ready: true}})
	if !NewClient(fullDescription(), dialerFor(conn)).Ready() {
		t.Error("Ready() = false, want true")
	}

	conn = newFakeConn(map[string]any{MethodReady: ReadyResult{Ready: false}})
	if NewClient(fullDescription(), dialerFor(conn)).Ready() {
		t.Error("Ready() = true, want false")
	}

	// A plugin that cannot be reached cannot serve a request either, so it
	// reports unready rather than propagating a failure into the chat path.
	broken := newFakeConn(nil)
	broken.failWith = errors.New("pipe closed")
	if NewClient(fullDescription(), dialerFor(broken)).Ready() {
		t.Error("Ready() on an unreachable plugin = true, want false")
	}
}

func TestSecretAndLoginAreForwarded(t *testing.T) {
	conn := newFakeConn(map[string]any{})
	client := NewClient(fullDescription(), dialerFor(conn))

	if err := client.SetSecret("acme-key"); err != nil {
		t.Fatalf("SetSecret() = %v, want nil", err)
	}
	var sent SecretRequest
	if err := json.Unmarshal(conn.params[MethodSetSecret], &sent); err != nil {
		t.Fatalf("secret request was not readable: %v", err)
	}
	if sent.Secret != "acme-key" {
		t.Errorf("secret sent = %q, want acme-key", sent.Secret)
	}

	// Login has no result: a plugin returning nothing is a success, because
	// the flow itself happens in the child process.
	if err := client.Login(); err != nil {
		t.Fatalf("Login() = %v, want nil", err)
	}
	if conn.calls[len(conn.calls)-1] != MethodLogin {
		t.Errorf("calls = %v, want login last", conn.calls)
	}
}

func TestFetchModels(t *testing.T) {
	conn := newFakeConn(map[string]any{
		MethodReady:       ReadyResult{Ready: true},
		MethodFetchModels: ModelsResult{Models: []string{"live-1"}},
	})
	client := NewClient(fullDescription(), dialerFor(conn))

	// Through the registry helper, the live list must win over the static one.
	got := llm.AvailableModels(context.Background(), client)
	if len(got) != 1 || got[0] != "live-1" {
		t.Errorf("AvailableModels() = %v, want the live list", got)
	}

	// An unready plugin is not asked: the static list is used instead, so a
	// plugin without credentials still populates the model dropdown.
	unready := newFakeConn(map[string]any{
		MethodReady:       ReadyResult{Ready: false},
		MethodFetchModels: ModelsResult{Models: []string{"live-1"}},
	})
	got = llm.AvailableModels(context.Background(), NewClient(fullDescription(), dialerFor(unready)))
	if len(got) != 2 || got[0] != "acme-large" {
		t.Errorf("AvailableModels() on an unready plugin = %v, want the static list", got)
	}
}

// A plugin that reports a JSON-RPC error must not be mistaken for one that
// answered.
func TestRPCErrorIsReported(t *testing.T) {
	conn := newFakeConn(map[string]any{})
	conn.rpcError = "model is not available"
	client := NewClient(fullDescription(), dialerFor(conn))

	_, err := client.Chat(context.Background(), nil, "acme-large")
	if err == nil {
		t.Fatal("Chat() = nil error, want the plugin's error")
	}
	if !strings.Contains(err.Error(), "model is not available") {
		t.Errorf("Chat() error = %v, want it to carry the plugin's message", err)
	}
}

// Rubbish, or nothing at all, must fail rather than reach the agent loop as an
// empty answer.
func TestMalformedResultsAreRejected(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		conn := newFakeConn(map[string]any{})
		conn.raw[MethodChat] = "not json at all"
		if _, err := NewClient(fullDescription(), dialerFor(conn)).Chat(context.Background(), nil, "m"); err == nil {
			t.Error("Chat() on an unreadable result = nil, want an error")
		}
	})

	t.Run("empty", func(t *testing.T) {
		conn := newFakeConn(map[string]any{})
		if _, err := NewClient(fullDescription(), dialerFor(conn)).Chat(context.Background(), nil, "m"); err == nil {
			t.Error("Chat() on an empty result = nil, want an error")
		}
	})

	t.Run("no response field", func(t *testing.T) {
		conn := newFakeConn(map[string]any{MethodChat: ChatResult{}})
		if _, err := NewClient(fullDescription(), dialerFor(conn)).Chat(context.Background(), nil, "m"); err == nil {
			t.Error("Chat() on a result without a response = nil, want an error")
		}
	})
}

// The process is started once and reused, which is what keeps a chat turn from
// paying for a spawn.
func TestProcessIsStartedOnceAndReused(t *testing.T) {
	conn := newFakeConn(map[string]any{
		MethodChat:  ChatResult{Response: &llm.Response{Content: "ok"}},
		MethodReady: ReadyResult{Ready: true},
	})
	client := NewClient(fullDescription(), dialerFor(conn))

	client.Ready()
	if _, err := client.Chat(context.Background(), nil, "m"); err != nil {
		t.Fatalf("Chat() = %v", err)
	}
	if conn.started != 1 {
		t.Errorf("started %d times, want 1", conn.started)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if conn.closed != 1 {
		t.Errorf("closed %d times, want 1", conn.closed)
	}
	// Closing twice is harmless: shutdown paths can overlap.
	if err := client.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
}

// A plugin that dies mid-request must not poison every later call: the next
// one starts a fresh process.
func TestCrashedPluginIsRestartedOnTheNextCall(t *testing.T) {
	dead := newFakeConn(nil)
	dead.failWith = errors.New("broken pipe")
	alive := newFakeConn(map[string]any{MethodChat: ChatResult{Response: &llm.Response{Content: "back"}}})

	conns := []Conn{dead, alive}
	var dialed int
	client := NewClient(fullDescription(), func() Conn {
		conn := conns[dialed]
		dialed++
		return conn
	})

	if _, err := client.Chat(context.Background(), nil, "m"); err == nil {
		t.Fatal("first Chat() = nil error, want the failure")
	}
	if dead.closed != 1 {
		t.Errorf("the dead connection was closed %d times, want 1", dead.closed)
	}

	resp, err := client.Chat(context.Background(), nil, "m")
	if err != nil {
		t.Fatalf("second Chat() = %v, want nil after a restart", err)
	}
	if resp.Content != "back" {
		t.Errorf("second Chat() = %q, want back", resp.Content)
	}
	if dialed != 2 {
		t.Errorf("dialed %d times, want 2", dialed)
	}
}
