package plugin

import (
	"context"
	"encoding/json"
	"fmt"
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
	// for every registered provider.
	readyTimeout = 5 * time.Second
	// secretTimeout bounds handing over a credential.
	secretTimeout = 15 * time.Second
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

	mu   sync.Mutex
	conn Conn
	// stop ends the process. The transport spawns the child with
	// exec.CommandContext, so the context handed to Start decides how long the
	// process lives — it must belong to the Client, never to a single call, or
	// the first call to finish would kill the plugin for the next one.
	stop context.CancelFunc
}

// NewClient builds a provider backed by the plugin that desc describes.
func NewClient(desc Description, dial Dialer) *Client {
	return &Client{desc: desc, dial: dial}
}

// Description returns what the plugin said about itself.
func (c *Client) Description() Description { return c.desc }

func (c *Client) Name() string { return c.desc.Name }

func (c *Client) Models() []string { return c.desc.Models }

// Close stops the plugin process if it is running.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *Client) closeLocked() error {
	if c.conn == nil {
		return nil
	}
	conn, stop := c.conn, c.stop
	c.conn, c.stop = nil, nil
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
		return fmt.Errorf("provider plugin %q did not stop", c.desc.Name)
	}
}

// call sends one request, starting the plugin if it is not running yet, and
// decodes the result into out. out may be nil for a method with no result.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.conn == nil {
		conn := c.dial()
		procCtx, stop := context.WithCancel(context.Background())
		if err := conn.Start(procCtx); err != nil {
			stop()
			c.mu.Unlock()
			return fmt.Errorf("starting provider plugin %q: %w", c.desc.Name, err)
		}
		c.conn, c.stop = conn, stop
		logger.Info("Provider plugin %s: started", c.desc.Name)
	}
	conn := c.conn
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	resp, err := conn.SendRequest(ctx, transport.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcp.NewRequestId(id),
		Method:  method,
		Params:  params,
	})
	if err != nil {
		// The plugin is gone or unusable: drop it so the next call retries
		// with a fresh process instead of talking to a dead pipe.
		c.mu.Lock()
		if c.conn == conn {
			c.closeLocked()
		}
		c.mu.Unlock()
		return fmt.Errorf("provider plugin %q: %s: %w", c.desc.Name, method, err)
	}
	if resp.Error != nil {
		return fmt.Errorf("provider plugin %q: %s: %s", c.desc.Name, method, resp.Error.Message)
	}
	if out == nil {
		return nil
	}
	if len(resp.Result) == 0 {
		return fmt.Errorf("provider plugin %q: %s: empty result", c.desc.Name, method)
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("provider plugin %q: %s: unreadable result: %w", c.desc.Name, method, err)
	}
	return nil
}

// Describe asks a plugin who it is. It is the only call made before the
// plugin is registered, and the only one whose result is cached.
func Describe(ctx context.Context, dial Dialer) (Description, error) {
	probe := &Client{dial: dial}
	defer probe.Close()

	var desc Description
	if err := probe.call(ctx, MethodDescribe, nil, &desc); err != nil {
		return Description{}, err
	}
	if desc.Name == "" {
		return Description{}, fmt.Errorf("provider plugin described itself without a name")
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
		return nil, fmt.Errorf("provider plugin %q returned no response", c.desc.Name)
	}
	return result.Response, nil
}

func (c *Client) ChatWithTools(ctx context.Context, messages []llm.Message, model string, toolDefs []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	if !c.desc.SupportsTools {
		return nil, nil, fmt.Errorf("provider plugin %q does not support native tool calls", c.desc.Name)
	}

	var result ChatResult
	err := c.call(ctx, MethodChatWithTools, ChatRequest{Messages: messages, Model: model, Tools: toolDefs}, &result)
	if err != nil {
		return nil, nil, err
	}
	if result.Response == nil {
		return nil, nil, fmt.Errorf("provider plugin %q returned no response", c.desc.Name)
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
		return fmt.Errorf("provider plugin %q takes no secret", c.desc.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), secretTimeout)
	defer cancel()
	return c.call(ctx, MethodSetSecret, SecretRequest{Secret: secret}, nil)
}

// Login runs the plugin's interactive login. The flow happens in the child
// process, which can open a loopback listener and a browser like any other
// process — the reason this mechanism was chosen over a sandboxed one.
//
// It carries no deadline of its own: the user is in the browser, and how long
// that takes is not the core's business.
func (c *Client) Login() error {
	if !c.desc.SupportsLogin {
		return fmt.Errorf("provider plugin %q has no interactive login", c.desc.Name)
	}
	return c.call(context.Background(), MethodLogin, nil, nil)
}

func (c *Client) FetchModels(ctx context.Context) ([]string, error) {
	if !c.desc.SupportsFetchModels {
		return nil, fmt.Errorf("provider plugin %q cannot fetch models", c.desc.Name)
	}

	var result ModelsResult
	if err := c.call(ctx, MethodFetchModels, nil, &result); err != nil {
		return nil, err
	}
	return result.Models, nil
}

// Compile-time proof that a plugin satisfies the contract the agent loop
// depends on.
var (
	_ llm.Provider          = (*Client)(nil)
	_ llm.ToolCallProvider  = (*Client)(nil)
	_ llm.ReadinessReporter = (*Client)(nil)
	_ llm.SecretSetter      = (*Client)(nil)
	_ llm.LoginStarter      = (*Client)(nil)
	_ llm.ModelFetcher      = (*Client)(nil)
)
