package mcpserver

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Proxy serves the OpenUAI MCP tools over stdio by forwarding every call to
// the MCP endpoint of the running app (streamable HTTP on the local API).
// It is what `openuai mcp` runs, for clients that launch MCP servers as
// commands (Claude Desktop, Claude Code with stdio servers...).
//
// The tools are announced even when the app is not reachable; calls then
// fail with an explanation, and the proxy reconnects on the next call.
type Proxy struct {
	url     string
	version string

	mu     sync.Mutex
	client *client.Client
	server *server.MCPServer
}

// NewProxy returns a proxy to the MCP endpoint at url
// (e.g. http://127.0.0.1:9120/mcp).
func NewProxy(url, version string) *Proxy {
	p := &Proxy{url: url, version: version}
	p.server = server.NewMCPServer(Name, version,
		server.WithToolCapabilities(false),
		server.WithInstructions("OpenUAI is a desktop AI agent running on this computer. "+
			"Use send_message to give it a task and get its answer; it keeps one shared conversation with its user."),
		server.WithRecovery(),
	)
	for _, t := range Tools() {
		p.server.AddTool(t, p.forward)
	}
	return p
}

// Serve runs the proxy on in/out until ctx ends or in is closed. Diagnostics
// go to errLog (never to out, which carries the protocol).
func (p *Proxy) Serve(ctx context.Context, in io.Reader, out io.Writer, errLog *log.Logger) error {
	defer p.drop()
	stdio := server.NewStdioServer(p.server)
	if errLog != nil {
		stdio.SetErrorLogger(errLog)
	}
	return stdio.Listen(ctx, in, out)
}

// connect returns the upstream client, connecting if needed.
func (p *Proxy) connect(ctx context.Context) (*client.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, nil
	}
	c, err := client.NewStreamableHttpClient(p.url)
	if err != nil {
		return nil, err
	}
	if err := c.Start(ctx); err != nil {
		c.Close()
		return nil, err
	}
	// Progress reported by the app goes on to our client (stdio has one).
	c.OnNotification(func(n mcp.JSONRPCNotification) {
		if n.Method == "notifications/progress" {
			p.server.SendNotificationToAllClients(n.Method, n.Params.AdditionalFields)
		}
	})
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "openuai-mcp-proxy", Version: p.version}
	if _, err := c.Initialize(ctx, init); err != nil {
		c.Close()
		return nil, err
	}
	p.client = c
	return c, nil
}

// drop closes the upstream client so the next call reconnects (the app may
// have restarted, which ends the MCP session).
func (p *Proxy) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		p.client.Close()
		p.client = nil
	}
}

func (p *Proxy) forward(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	c, err := p.connect(ctx)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"Cannot reach OpenUAI at %s: %v. Make sure the OpenUAI app is running with the local API enabled (api_enabled in ~/.config/openuai/config.json).",
			p.url, err)), nil
	}
	up := mcp.CallToolRequest{}
	up.Params.Name = req.Params.Name
	up.Params.Arguments = req.Params.Arguments
	up.Params.Meta = req.Params.Meta
	res, err := c.CallTool(ctx, up)
	if err != nil {
		p.drop()
		return mcp.NewToolResultError(fmt.Sprintf("OpenUAI call failed: %v", err)), nil
	}
	return res, nil
}
