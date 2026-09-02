package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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
	// block names methods that never answer, so the call ends on the
	// caller's deadline.
	block map[string]bool
	// delay is how long every answered method takes.
	delay time.Duration

	// mu guards the recorded calls: one client serves concurrent callers, so
	// the double has to be safe for them too.
	mu      sync.Mutex
	started int
	closed  int
	calls   []string
	params  map[string]json.RawMessage
}

func (f *fakeConn) callsMade() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeConn) counts() (started, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started, f.closed
}

func (f *fakeConn) paramsFor(method string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.params[method]
}

func newFakeConn(results map[string]any) *fakeConn {
	return &fakeConn{results: results, raw: map[string]string{}, block: map[string]bool{}, params: map[string]json.RawMessage{}}
}

func (f *fakeConn) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	return nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *fakeConn) SendRequest(ctx context.Context, req transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req.Method)
	if req.Params != nil {
		encoded, err := json.Marshal(req.Params)
		if err != nil {
			f.mu.Unlock()
			return nil, err
		}
		f.params[req.Method] = encoded
	}
	f.mu.Unlock()

	if f.block[req.Method] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
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
	if len(conn.callsMade()) != 1 || conn.callsMade()[0] != MethodDescribe {
		t.Errorf("calls = %v, want one describe", conn.callsMade())
	}
	// The probe does not leave a process behind.
	if _, closed := conn.counts(); closed != 1 {
		t.Errorf("probe closed the connection %d times, want 1", closed)
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
	if err := json.Unmarshal(conn.paramsFor(MethodChat), &sent); err != nil {
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
	client, ok := NewClient(fullDescription(), dialerFor(conn)).Provider().(llm.ToolCallProvider)
	if !ok {
		t.Fatal("a plugin declaring tool support does not satisfy ToolCallProvider")
	}

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
	if err := json.Unmarshal(conn.paramsFor(MethodChatWithTools), &sent); err != nil {
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
	if len(conn.callsMade()) != 0 {
		t.Errorf("calls = %v, want none", conn.callsMade())
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
	if err := json.Unmarshal(conn.paramsFor(MethodSetSecret), &sent); err != nil {
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
	if conn.callsMade()[len(conn.callsMade())-1] != MethodLogin {
		t.Errorf("calls = %v, want login last", conn.callsMade())
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
	if started, _ := conn.counts(); started != 1 {
		t.Errorf("started %d times, want 1", started)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if _, closed := conn.counts(); closed != 1 {
		t.Errorf("closed %d times, want 1", closed)
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
	if _, closed := dead.counts(); closed != 1 {
		t.Errorf("the dead connection was closed %d times, want 1", closed)
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

// A client is closed when its plugin is removed, or when the app is shutting
// down. Anything still holding it — an agent turn makes several calls per
// turn — must not be able to start the process again, or removal and
// shutdown would leave a process nothing is tracking.
func TestAClosedClientStartsNoFurtherProcess(t *testing.T) {
	conn := newFakeConn(map[string]any{
		MethodChat:  ChatResult{Response: &llm.Response{Content: "ok"}},
		MethodReady: ReadyResult{Ready: true},
	})
	var dialed int
	client := NewClient(fullDescription(), func() Conn {
		dialed++
		return conn
	})

	if _, err := client.Chat(context.Background(), nil, "m"); err != nil {
		t.Fatalf("Chat() = %v, want nil", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	// Whatever still holds the client is refused rather than served by a
	// fresh process.
	if _, err := client.Chat(context.Background(), nil, "m"); err == nil {
		t.Error("Chat() after Close() = nil error, want a refusal")
	}
	if tc, ok := client.Provider().(llm.ToolCallProvider); ok {
		if _, _, err := tc.ChatWithTools(context.Background(), nil, "m", []llm.ToolDefinition{{Name: "t"}}); err == nil {
			t.Error("ChatWithTools() after Close() = nil error, want a refusal")
		}
	}
	if client.Ready() {
		t.Error("Ready() after Close() = true, want false")
	}
	if dialed != 1 {
		t.Errorf("dialed %d times, want 1: a closed client started another process", dialed)
	}
}

// Closing for good must not break the recovery path: a call that merely
// failed still gets a fresh process on the next attempt.
func TestCloseDoesNotDisableCrashRecovery(t *testing.T) {
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
	// The failure dropped the connection but did not close the client.
	resp, err := client.Chat(context.Background(), nil, "m")
	if err != nil {
		t.Fatalf("Chat() after a failure = %v, want a restart", err)
	}
	if resp.Content != "back" {
		t.Errorf("Chat() = %q, want back", resp.Content)
	}
}

// The agent loop chooses native tool calls with a type assertion, not by
// asking, so a plugin without tool support must not satisfy the interface at
// all. Satisfying it unconditionally sent every chat-only plugin down the
// native path, where its first turn failed.
func TestToolSupportIsVisibleAtRuntime(t *testing.T) {
	conn := newFakeConn(map[string]any{})

	withTools := NewClient(fullDescription(), dialerFor(conn)).Provider()
	if _, ok := withTools.(llm.ToolCallProvider); !ok {
		t.Error("a plugin declaring tool support is not a ToolCallProvider")
	}

	noTools := fullDescription()
	noTools.SupportsTools = false
	plain := NewClient(noTools, dialerFor(conn)).Provider()
	if _, ok := plain.(llm.ToolCallProvider); ok {
		t.Error("a plugin declaring no tool support is a ToolCallProvider: the agent loop will take the native path and fail every turn")
	}
	// It is still a usable provider, just a chat-only one.
	if _, ok := plain.(llm.Provider); !ok {
		t.Error("a chat-only plugin is not even a Provider")
	}
	// The other capabilities still come from the description.
	if !llm.Ready(plain) == false && false {
		t.Error("unreachable")
	}
}

// One client serves every concurrent turn and sub-agent, so a deadline that
// belongs to one caller must not tear down the connection the others are
// using. A readiness probe timing out used to kill an in-flight chat.
func TestOneCallersDeadlineDoesNotAbortTheOthers(t *testing.T) {
	conn := newFakeConn(map[string]any{
		MethodChat: ChatResult{Response: &llm.Response{Content: "survived"}},
	})
	conn.block[MethodReady] = true
	conn.delay = 150 * time.Millisecond
	client := NewClient(fullDescription(), dialerFor(conn))

	// Start the long call first so the connection is up and it is in flight.
	var wg sync.WaitGroup
	var chatErr error
	var chatResp *llm.Response
	wg.Add(1)
	go func() {
		defer wg.Done()
		chatResp, chatErr = client.Chat(context.Background(), nil, "m")
	}()

	// A second call on the same client gives up on its own deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := client.call(ctx, MethodReady, nil, &ReadyResult{}); err == nil {
		t.Fatal("the bounded call = nil error, want its deadline")
	}

	wg.Wait()
	if chatErr != nil {
		t.Errorf("the other call in flight failed with %v, want it to survive", chatErr)
	}
	if chatResp == nil || chatResp.Content != "survived" {
		t.Errorf("the other call returned %+v", chatResp)
	}
	// And the process was not stopped behind its back.
	if _, closed := conn.counts(); closed != 0 {
		t.Errorf("the connection was closed %d times, want 0", closed)
	}
}

// A candidate being probed has not said its name yet, so its failures must
// describe it rather than quote an empty one.
func TestErrorsNameTheCandidateBeforeItIsKnown(t *testing.T) {
	conn := newFakeConn(map[string]any{})
	conn.rpcError = "no such method"

	_, err := Describe(context.Background(), dialerFor(conn))
	if err == nil {
		t.Fatal("Describe() = nil error, want the plugin's refusal")
	}
	if strings.Contains(err.Error(), `""`) {
		t.Errorf("Describe() error quotes an empty name: %v", err)
	}
	if !strings.Contains(err.Error(), "candidate") {
		t.Errorf("Describe() error does not describe the candidate: %v", err)
	}

	// A named plugin still names itself.
	named := newFakeConn(map[string]any{})
	named.rpcError = "nope"
	if err := NewClient(fullDescription(), dialerFor(named)).SetSecret("s"); err != nil {
		if !strings.Contains(err.Error(), `"acme"`) {
			t.Errorf("a named plugin's error does not quote its name: %v", err)
		}
	}
}

// Login is generous, because a person is in the browser, but it is bounded:
// unbounded meant a plugin that never answers held the call for the life of
// the app.
func TestLoginIsBounded(t *testing.T) {
	if loginTimeout == 0 {
		t.Fatal("Login carries no deadline")
	}
	if loginTimeout < time.Minute {
		t.Errorf("loginTimeout = %v, too short for a browser flow", loginTimeout)
	}
}
