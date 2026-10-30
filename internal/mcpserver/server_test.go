package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// fakeApp is a backend that records what it is asked.
type fakeApp struct {
	mu       sync.Mutex
	sent     []string
	history  []map[string]string
	stopped  atomic.Int32
	cleared  atomic.Int32
	delay    time.Duration
	failWith string
}

func (f *fakeApp) backend() Backend {
	return Backend{
		SendMessage: func(msg string) Reply {
			time.Sleep(f.delay)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.sent = append(f.sent, msg)
			if f.failWith != "" {
				return Reply{Error: f.failWith, Refused: true}
			}
			answer := "answer to: " + msg
			f.history = append(f.history,
				map[string]string{"role": "user", "content": msg},
				map[string]string{"role": "assistant", "content": answer})
			return Reply{Content: answer, Model: "fake-model", InputTokens: 3, OutputTokens: 4}
		},
		History: func() any {
			f.mu.Lock()
			defer f.mu.Unlock()
			return append([]map[string]string{}, f.history...)
		},
		NewConversation: func() { f.cleared.Add(1) },
		Stop:            func() { f.stopped.Add(1) },
		Status:          func() any { return map[string]any{"provider": "fake", "model": "fake-model"} },
	}
}

func (f *fakeApp) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func startClient(t *testing.T, c *client.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1"}
	res, err := c.Initialize(ctx, init)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if res.ServerInfo.Name != Name {
		t.Fatalf("server name = %q, want %q", res.ServerInfo.Name, Name)
	}
}

func call(t *testing.T, c *client.Client, name string, args map[string]any) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return resultText(res), res.IsError
}

func resultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func checkTools(t *testing.T, c *client.Client) {
	t.Helper()
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{ToolSendMessage, ToolGetConversation, ToolNewConversation, ToolStopTask, ToolGetStatus} {
		if !got[want] {
			t.Errorf("tool %s missing (got %v)", want, got)
		}
	}
}

func TestInProcessTools(t *testing.T) {
	app := &fakeApp{}
	c, err := client.NewInProcessClient(New("test", app.backend()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	startClient(t, c)
	checkTools(t, c)

	text, isErr := call(t, c, ToolSendMessage, map[string]any{"message": "hello"})
	if isErr || text != "answer to: hello" {
		t.Fatalf("send_message = %q (error %v)", text, isErr)
	}
	call(t, c, ToolSendMessage, map[string]any{"message": "second"})

	text, _ = call(t, c, ToolGetConversation, map[string]any{"limit": 2})
	var hist []map[string]string
	if err := json.Unmarshal([]byte(text), &hist); err != nil {
		t.Fatalf("history is not JSON: %v\n%s", err, text)
	}
	if len(hist) != 2 || hist[0]["content"] != "second" {
		t.Fatalf("limited history = %v", hist)
	}
	text, _ = call(t, c, ToolGetConversation, map[string]any{"limit": 0})
	if err := json.Unmarshal([]byte(text), &hist); err != nil || len(hist) != 4 {
		t.Fatalf("full history = %s", text)
	}

	if text, _ = call(t, c, ToolGetStatus, nil); !strings.Contains(text, "fake-model") {
		t.Fatalf("status = %s", text)
	}
	call(t, c, ToolStopTask, nil)
	call(t, c, ToolNewConversation, nil)
	if app.stopped.Load() != 1 || app.cleared.Load() != 1 {
		t.Fatalf("stop=%d clear=%d", app.stopped.Load(), app.cleared.Load())
	}

	// A missing message is a tool error, not a run.
	if _, isErr := call(t, c, ToolSendMessage, map[string]any{}); !isErr {
		t.Fatal("empty message should be an error")
	}
	if n := app.sentCount(); n != 2 {
		t.Fatalf("sent %d messages, want 2", n)
	}
}

func TestSendMessageErrorIsToolError(t *testing.T) {
	app := &fakeApp{failWith: "Blocked by a hook: nope"}
	c, _ := client.NewInProcessClient(New("test", app.backend()))
	defer c.Close()
	startClient(t, c)
	text, isErr := call(t, c, ToolSendMessage, map[string]any{"message": "x"})
	if !isErr || !strings.Contains(text, "Blocked by a hook") {
		t.Fatalf("got %q (error %v)", text, isErr)
	}
}

func TestStreamableHTTPWithProgress(t *testing.T) {
	old := ProgressInterval
	ProgressInterval = 20 * time.Millisecond
	defer func() { ProgressInterval = old }()

	app := &fakeApp{delay: 200 * time.Millisecond}
	ts := httptest.NewServer(HTTPHandler(New("test", app.backend())))
	defer ts.Close()

	c, err := client.NewStreamableHttpClient(ts.URL + Path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var progress atomic.Int32
	c.OnNotification(func(n mcp.JSONRPCNotification) {
		if n.Method == "notifications/progress" {
			progress.Add(1)
		}
	})
	startClient(t, c)
	checkTools(t, c)

	req := mcp.CallToolRequest{}
	req.Params.Name = ToolSendMessage
	req.Params.Arguments = map[string]any{"message": "over http"}
	req.Params.Meta = &mcp.Meta{ProgressToken: "p1"}
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := resultText(res); got != "answer to: over http" {
		t.Fatalf("answer = %q", got)
	}
	if progress.Load() == 0 {
		t.Fatal("no progress notifications during a long task")
	}
}

func TestLocalOnly(t *testing.T) {
	h := LocalOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	cases := []struct {
		host, origin string
		want         int
	}{
		{"127.0.0.1:9120", "", http.StatusOK},
		{"localhost:9120", "http://localhost:3000", http.StatusOK},
		{"[::1]:9120", "", http.StatusOK},
		{"evil.example:9120", "", http.StatusForbidden},                  // DNS rebinding
		{"127.0.0.1:9120", "https://evil.example", http.StatusForbidden}, // cross-site
		{"127.0.0.1:9120", "null", http.StatusForbidden},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, Path, nil)
		r.Host = tc.host
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("host=%s origin=%s: got %d, want %d", tc.host, tc.origin, w.Code, tc.want)
		}
	}
}

// stdioClient runs the proxy on pipes and returns a client talking to it.
func stdioClient(t *testing.T, upstream string) *client.Client {
	t.Helper()
	inR, inW := io.Pipe()   // client -> proxy
	outR, outW := io.Pipe() // proxy -> client
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = NewProxy(upstream, "test").Serve(ctx, inR, outW, nil)
		outW.Close()
	}()
	c := client.NewClient(transport.NewIO(outR, inW, nil))
	t.Cleanup(func() {
		c.Close()
		cancel()
		inW.Close()
		<-done
	})
	startClient(t, c)
	return c
}

func TestStdioProxy(t *testing.T) {
	app := &fakeApp{}
	ts := httptest.NewServer(HTTPHandler(New("test", app.backend())))
	defer ts.Close()

	c := stdioClient(t, ts.URL+Path)
	checkTools(t, c)
	text, isErr := call(t, c, ToolSendMessage, map[string]any{"message": "via stdio"})
	if isErr || text != "answer to: via stdio" {
		t.Fatalf("send_message via proxy = %q (error %v)", text, isErr)
	}
}

func TestStdioProxyAppNotRunning(t *testing.T) {
	// A port nobody listens on.
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL + Path
	ts.Close()

	c := stdioClient(t, url)
	checkTools(t, c) // tools are announced anyway
	text, isErr := call(t, c, ToolGetStatus, nil)
	if !isErr || !strings.Contains(text, "Cannot reach OpenUAI") {
		t.Fatalf("got %q (error %v)", text, isErr)
	}
}
