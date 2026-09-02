package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"openuai/internal/llm"
	"openuai/internal/logger"
)

// A plugin that answers with anything other than a JSON-RPC reply does not
// fail: nothing ever arrives, and the call ends on its deadline. So every
// call the core makes on its own behalf carries one.
const (
	// readyTimeout bounds a readiness check, which the settings screen makes
	// for the active provider. It bounds the whole call, waiting for another
	// caller's process start included — see Client.starting.
	readyTimeout = 5 * time.Second
	// secretTimeout bounds handing over a credential.
	secretTimeout = 15 * time.Second
	// loginTimeout bounds an interactive login. The flow is a person in a
	// browser, so it is generous, but unbounded means a plugin that never
	// answers holds the call for the life of the app — the in-tree OAuth flow
	// caps its own wait for the same reason.
	loginTimeout = 5 * time.Minute
	// closeGrace is how long a plugin is given to exit on its own once its
	// stdin is closed. The transport waits on the process with no deadline of
	// its own, so a plugin that ignores the close would hang shutdown for
	// good; after the grace period it is killed.
	closeGrace = 2 * time.Second
)

// Conn is the request/response channel to a running plugin. The stdio
// transport satisfies it; tests substitute their own.
type Conn interface {
	Start(ctx context.Context) error
	SendRequest(ctx context.Context, req transport.JSONRPCRequest) (*transport.JSONRPCResponse, error)
	Close() error
}

// Dialer opens a channel to a plugin. It is separate from Client so a plugin
// can be reopened after it dies, and so tests need no child process.
type Dialer func() Conn

// StdioDialer runs the plugin as a child process and speaks to it over
// stdin/stdout.
func StdioDialer(command string, args []string, env map[string]string) Dialer {
	envSlice := make([]string, 0, len(env))
	for k, v := range env {
		envSlice = append(envSlice, k+"="+v)
	}
	return func() Conn {
		return transport.NewStdio(command, envSlice, args...)
	}
}

// Client is a model provider that lives in another process. It implements
// llm.Provider and llm.ToolCallProvider, plus whichever optional capabilities
// the plugin said it supports.
//
// The process is started on first use and kept running. A failed call leaves
// the connection closed so the next one starts a fresh process, which is what
// makes a crashed plugin recoverable without restarting the core.
type Client struct {
	desc   Description
	dial   Dialer
	nextID int64

	// starting serializes starting the process, so a connection is built once
	// and prepared before anyone can be given it. It is held across the pipe,
	// which mu deliberately is not: Close takes mu alone and stays responsive
	// while a process is starting.
	//
	// It is a channel rather than a mutex because a caller with a deadline has
	// to be able to give up waiting for it. sync.Mutex cannot be acquired with
	// a context, so a readiness probe waited out another caller's credential
	// restore before its own deadline began counting — measured at 14.7 s
	// against a 5 s bound, on a call the settings screen makes synchronously.
	starting chan struct{}

	mu   sync.Mutex
	conn Conn
	// stop ends the process. The transport spawns the child with
	// exec.CommandContext, so the context handed to Start decides how long the
	// process lives — it must belong to the Client, never to a single call, or
	// the first call to finish would kill the plugin for the next one.
	stop context.CancelFunc
	// secret is the credential this session handed over, kept so a process
	// that had to be restarted can be given it again. The core persists
	// nothing for a plugin — a plugin owns its own storage — so without this
	// a crash silently produced an unauthenticated provider.
	secret string
	// stopped records that the owner closed this client for good. A call that
	// merely failed leaves the connection dropped, and the next one starts a
	// fresh process on purpose; a client that was closed must not do that, or
	// a plugin removed — or an app shutting down — while a turn is still in
	// flight would restart a process nothing is tracking any more.
	stopped bool
}

// NewClient builds a provider backed by the plugin that desc describes.
func NewClient(desc Description, dial Dialer) *Client {
	return &Client{desc: desc, dial: dial, starting: make(chan struct{}, 1)}
}

// acquireStart takes the right to start the process, or gives up when ctx
// does. A caller that gives up here reports what a caller whose request timed
// out reports: the plugin did not answer in the time it was given.
func (c *Client) acquireStart(ctx context.Context) error {
	select {
	case c.starting <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s: waiting for the process to start: %w", c.label(), ctx.Err())
	}
}

func (c *Client) releaseStart() { <-c.starting }

// Description returns what the plugin said about itself.
func (c *Client) Description() Description { return c.desc }

func (c *Client) Name() string { return c.desc.Name }

func (c *Client) Models() []string { return c.desc.Models }

// Close stops the plugin process if it is running, for good: a closed client
// serves no further calls and starts no further processes.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	return c.closeLocked()
}

func (c *Client) closeLocked() error {
	if c.conn == nil {
		return nil
	}
	conn, stop := c.conn, c.stop
	c.conn, c.stop = nil, nil
	return c.shutdown(conn, stop)
}

// shutdown stops one connection and the process behind it. It takes them as
// arguments rather than reading the client, so it also serves a connection
// that was started but never published.
func (c *Client) shutdown(conn Conn, stop context.CancelFunc) error {
	if stop == nil {
		stop = func() {}
	}

	// Closing shuts stdin and waits for the process. Give a well-behaved
	// plugin the chance to exit cleanly, then stop waiting for one that will
	// not.
	closed := make(chan error, 1)
	go func() { closed <- conn.Close() }()

	select {
	case err := <-closed:
		stop()
		return err
	case <-time.After(closeGrace):
		logger.Info("Provider plugin %s: did not exit, stopping it", c.desc.Name)
		stop()
	}

	select {
	case err := <-closed:
		return err
	case <-time.After(closeGrace):
		return fmt.Errorf("%s did not stop", c.label())
	}
}

// label names this plugin for a message. A candidate being probed has not
// said its name yet, so it is described rather than quoted as empty.
func (c *Client) label() string {
	if c.desc.Name == "" {
		return "provider plugin candidate"
	}
	return fmt.Sprintf("provider plugin %q", c.desc.Name)
}

// connect returns a live connection, starting the plugin if it is not running
// yet.
//
// A connection is published on the client only once it is ready to be used:
// the credential a restarted process never received is handed over first, on
// a connection nothing else can reach yet. Publishing first and restoring
// afterwards needed the lock dropped in between, which meant a caller could
// read a connection that a concurrent failure or a Close had already nilled —
// a nil dereference in request — and meant another call could reach the
// process ahead of its credential.
func (c *Client) connect(ctx context.Context) (Conn, error) {
	if err := c.acquireStart(ctx); err != nil {
		return nil, err
	}
	defer c.releaseStart()

	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil, fmt.Errorf("%s has been stopped", c.label())
	}
	if c.conn != nil {
		conn := c.conn
		c.mu.Unlock()
		return conn, nil
	}
	restored := c.secret
	c.mu.Unlock()

	conn := c.dial()
	procCtx, stop := context.WithCancel(context.Background())
	if err := conn.Start(procCtx); err != nil {
		stop()
		return nil, fmt.Errorf("starting %s: %w", c.label(), err)
	}
	logger.Info("Provider plugin %s: started", c.desc.Name)

	// The transport creates the child's stderr pipe but never reads it. Left
	// alone, the child blocks in write(2) as soon as the pipe buffer fills —
	// 64 KiB on Linux, less on Windows — and stops answering. It also means
	// the plugin's own diagnostics never reach the log.
	if src, ok := conn.(stderrReader); ok {
		go drainStderr(c.desc.Name, src.Stderr())
	}

	// A restarted process is a fresh program: it has never been given the
	// credential this session already supplied, and would come back
	// unauthenticated without a word.
	if restored != "" {
		secretCtx, cancel := context.WithTimeout(ctx, secretTimeout)
		err := c.send(secretCtx, conn, MethodSetSecret, SecretRequest{Secret: restored}, nil)
		cancel()
		if err != nil {
			// Nothing else has reached this process, so it is still ours to
			// discard. Reporting the failure beats publishing a provider that
			// would reject every turn for a reason nobody can see.
			c.shutdown(conn, stop)
			return nil, fmt.Errorf("restoring the credential of %s after a restart: %w", c.label(), err)
		}
		logger.Info("Provider plugin %s: credential restored after a restart", c.desc.Name)
	}

	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		// Closed for good while this process was starting. Close found no
		// connection to stop, because it had not been published, so stopping
		// it belongs here — otherwise removing a plugin, or quitting, would
		// leave the process behind.
		c.shutdown(conn, stop)
		return nil, fmt.Errorf("%s has been stopped", c.label())
	}
	c.conn, c.stop = conn, stop
	c.mu.Unlock()
	return conn, nil
}

// call sends one request, starting the plugin if it is not running yet, and
// decodes the result into out. out may be nil for a method with no result.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	conn, err := c.connect(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	resp, err := c.request(ctx, conn, id, method, params)
	if err != nil {
		// A deadline or a cancellation belongs to this caller and says nothing
		// about the plugin. The connection is shared — one client serves every
		// concurrent turn and sub-agent — so closing it here would abort every
		// other call in flight, which is how a readiness probe timing out used
		// to kill a chat.
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %s: %w", c.label(), method, err)
		}
		// Anything else means the plugin is gone or unusable: drop it so the
		// next call retries with a fresh process instead of a dead pipe.
		c.mu.Lock()
		if c.conn == conn {
			c.closeLocked()
		}
		c.mu.Unlock()
		return fmt.Errorf("%s: %s: %w", c.label(), method, err)
	}
	if resp.Error != nil {
		return fmt.Errorf("%s: %s: %s", c.label(), method, resp.Error.Message)
	}
	if out == nil {
		return nil
	}
	if len(resp.Result) == 0 {
		return fmt.Errorf("%s: %s: empty result", c.label(), method)
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("%s: %s: unreadable result: %w", c.label(), method, err)
	}
	return nil
}

// stderrReader is implemented by a connection that exposes the child's
// stderr. The stdio transport does; a test double need not.
type stderrReader interface {
	Stderr() io.Reader
}

// maxStderrLine bounds how much of one line is logged, so a plugin writing
// without newlines cannot turn the log into its buffer. It bounds the log, not
// the reading: what is over the bound is discarded and the drain carries on.
const maxStderrLine = 4096

// drainStderr copies a plugin's stderr into the log until the pipe closes.
//
// It must never stop reading while the process lives. bufio.Scanner cannot be
// used for that: it abandons the scan with ErrTooLong as soon as a token
// exceeds its buffer, so one line over the cap ended the drain for good and
// left the child blocked in write(2) on everything it logged afterwards — the
// very deadlock the drain exists to prevent, reached through the cap added to
// bound it. A stack trace, a dumped request body or an HTTP library's JSON
// error object is all it takes.
func drainStderr(name string, src io.Reader) {
	if src == nil {
		return
	}
	reader := bufio.NewReader(src)
	for {
		line, err := readBoundedLine(reader)
		if line != "" {
			logger.Info("Provider plugin %s: %s", name, line)
		}
		if err != nil {
			// A read error here is the pipe closing with the process, which
			// is ordinary, so it is not reported as a failure.
			return
		}
	}
}

// readBoundedLine reads one line, keeping at most maxStderrLine bytes of it
// and discarding the rest. A line longer than the reader's buffer arrives in
// several pieces, so it reads until the newline instead of giving up on it.
func readBoundedLine(r *bufio.Reader) (string, error) {
	var kept []byte
	dropped := 0
	for {
		chunk, err := r.ReadSlice('\n')
		if err == nil {
			chunk = bytes.TrimRight(chunk, "\r\n")
		}
		if room := maxStderrLine - len(kept); room < len(chunk) {
			if room > 0 {
				kept = append(kept, chunk[:room]...)
			}
			dropped += len(chunk) - max(room, 0)
		} else {
			kept = append(kept, chunk...)
		}
		// ErrBufferFull means the line does not fit the reader's buffer, not
		// that the pipe is done: keep going until the newline or a real error.
		if err == bufio.ErrBufferFull {
			continue
		}
		line := string(kept)
		if dropped > 0 {
			line = fmt.Sprintf("%s… (%d more bytes on this line)", line, dropped)
		}
		return line, err
	}
}

// request puts one JSON-RPC request on conn.
func (c *Client) request(ctx context.Context, conn Conn, id int64, method string, params any) (*transport.JSONRPCResponse, error) {
	return conn.SendRequest(ctx, transport.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcp.NewRequestId(id),
		Method:  method,
		Params:  params,
	})
}

// send makes one call on a connection the caller already holds, without
// touching the client's connection state. It exists so a restart can restore
// a credential without re-entering call.
func (c *Client) send(ctx context.Context, conn Conn, method string, params any, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	resp, err := c.request(ctx, conn, id, method, params)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", c.label(), method, err)
	}
	if resp.Error != nil {
		return fmt.Errorf("%s: %s: %s", c.label(), method, resp.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

// Describe asks a plugin who it is. It is the only call made before the
// plugin is registered, and the only one whose result is cached.
func Describe(ctx context.Context, dial Dialer) (Description, error) {
	probe := NewClient(Description{}, dial)
	defer probe.Close()

	var desc Description
	if err := probe.call(ctx, MethodDescribe, nil, &desc); err != nil {
		return Description{}, err
	}
	if desc.Name == "" {
		return Description{}, fmt.Errorf("provider plugin described itself without a name")
	}
	if err := desc.validate(); err != nil {
		return Description{}, err
	}
	return desc, nil
}

func (c *Client) Chat(ctx context.Context, messages []llm.Message, model string) (*llm.Response, error) {
	var result ChatResult
	err := c.call(ctx, MethodChat, ChatRequest{Messages: messages, Model: model}, &result)
	if err != nil {
		return nil, err
	}
	if result.Response == nil {
		return nil, fmt.Errorf("%s returned no response", c.label())
	}
	return result.Response, nil
}

// ProviderPlugin is a provider that runs as a child process, which is what
// Provider returns. The core tells a plugin from a compiled-in provider by
// asking for this interface: a plugin has a process to stop when the app quits
// or the plugin is removed, and a compiled-in provider does not.
//
// The marker method is unexported, so only this package can satisfy it. An
// interface of Close alone would say "owns something to shut down" while being
// read as "runs as a child process": the day a compiled-in provider grows a
// Close — an HTTP client with an idle connection pool is enough — it would be
// stopped on shutdown, which is probably harmless, and dropped from the
// provider map, which is not. Naming the concrete type instead is not an
// option either, since Provider returns one of two shapes.
type ProviderPlugin interface {
	llm.Provider
	// Close stops the child process, for good.
	Close() error
	// runsAsAChildProcess cannot be implemented outside this package.
	runsAsAChildProcess()
}

func (c *Client) runsAsAChildProcess() {}

// Provider returns the client in the shape the core should hold it.
//
// The agent loop decides whether to use native tool calls with a type
// assertion, `provider.(llm.ToolCallProvider)`, not by asking. A client that
// implemented ChatWithTools unconditionally would therefore satisfy that
// probe even for a plugin that declared no tool support, take the native path
// and fail every turn on its first iteration. So the method lives on a type
// that only exists when the plugin said it has tools.
func (c *Client) Provider() ProviderPlugin {
	if c.desc.SupportsTools {
		return toolCallClient{c}
	}
	return c
}

// toolCallClient is a plugin that declared native tool support.
type toolCallClient struct{ *Client }

func (t toolCallClient) ChatWithTools(ctx context.Context, messages []llm.Message, model string, toolDefs []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	c := t.Client

	var result ChatResult
	err := c.call(ctx, MethodChatWithTools, ChatRequest{Messages: messages, Model: model, Tools: toolDefs}, &result)
	if err != nil {
		return nil, nil, err
	}
	if result.Response == nil {
		return nil, nil, fmt.Errorf("%s returned no response", c.label())
	}
	return result.Response, result.ToolCalls, nil
}

// Ready asks the plugin whether it holds its credentials. A plugin that does
// not answer readiness is taken to be ready, matching the in-tree default.
func (c *Client) Ready() bool {
	if !c.desc.SupportsReady {
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
	defer cancel()

	var result ReadyResult
	if err := c.call(ctx, MethodReady, nil, &result); err != nil {
		// A plugin that cannot be reached cannot serve a request either.
		logger.Info("Provider plugin %s: readiness check failed: %s", c.desc.Name, err.Error())
		return false
	}
	return result.Ready
}

func (c *Client) SetSecret(secret string) error {
	if !c.desc.SupportsSecret {
		return fmt.Errorf("%s takes no secret", c.label())
	}

	ctx, cancel := context.WithTimeout(context.Background(), secretTimeout)
	defer cancel()
	if err := c.call(ctx, MethodSetSecret, SecretRequest{Secret: secret}, nil); err != nil {
		return err
	}

	// Remembered for this session only, so a restarted process can be given
	// it again. It is not written anywhere: a plugin owns its own storage.
	c.mu.Lock()
	c.secret = secret
	c.mu.Unlock()
	return nil
}

// Login runs the plugin's interactive login. The flow happens in the child
// process, which can open a loopback listener and a browser like any other
// process — the reason this mechanism was chosen over a sandboxed one.
//
// The wait is generous, because a person is in the browser, but it is bounded:
// a plugin that never answers must not hold the call forever.
func (c *Client) Login() error {
	if !c.desc.SupportsLogin {
		return fmt.Errorf("%s has no interactive login", c.label())
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	defer cancel()
	return c.call(ctx, MethodLogin, nil, nil)
}

func (c *Client) FetchModels(ctx context.Context) ([]string, error) {
	if !c.desc.SupportsFetchModels {
		return nil, fmt.Errorf("%s cannot fetch models", c.label())
	}

	var result ModelsResult
	if err := c.call(ctx, MethodFetchModels, nil, &result); err != nil {
		return nil, err
	}
	return result.Models, nil
}

// Compile-time proof that a plugin satisfies the contract the agent loop
// depends on. Note that ToolCallProvider is satisfied by toolCallClient and
// deliberately NOT by Client: see Provider.
var (
	_ llm.Provider          = (*Client)(nil)
	_ llm.ToolCallProvider  = toolCallClient{}
	_ ProviderPlugin        = (*Client)(nil)
	_ ProviderPlugin        = toolCallClient{}
	_ llm.ReadinessReporter = (*Client)(nil)
	_ llm.SecretSetter      = (*Client)(nil)
	_ llm.LoginStarter      = (*Client)(nil)
	_ llm.ModelFetcher      = (*Client)(nil)
)
