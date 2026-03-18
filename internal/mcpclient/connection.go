package mcpclient

import (
	"context"
	"fmt"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"openuai/internal/logger"
)

// Connection manages a single connection to an MCP server via stdio.
type Connection struct {
	name    string
	command string
	args    []string
	env     []string

	client *client.Client
	mu     sync.Mutex

	tools     []mcp.Tool
	resources []mcp.Resource

	// subscribeURIs are the resource URIs to subscribe to for notifications.
	subscribeURIs []string

	// onResourceUpdated is called when a subscribed resource is updated.
	onResourceUpdated func(conn *Connection, uri string)
}

// NewConnection creates a new MCP server connection.
func NewConnection(name, command string, args []string, env map[string]string, subscribeURIs []string) *Connection {
	envSlice := make([]string, 0, len(env))
	for k, v := range env {
		envSlice = append(envSlice, k+"="+v)
	}
	return &Connection{
		name:          name,
		command:       command,
		args:          args,
		env:           envSlice,
		subscribeURIs: subscribeURIs,
	}
}

// Name returns the connection name.
func (c *Connection) Name() string { return c.name }

// Start launches the MCP server process, initializes the connection,
// discovers tools and resources, and subscribes to resource updates.
func (c *Connection) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	logger.Info("MCP[%s]: starting server %s %v", c.name, c.command, c.args)

	stdioTransport := transport.NewStdio(c.command, c.env, c.args...)
	c.client = client.NewClient(stdioTransport)

	if err := c.client.Start(ctx); err != nil {
		return fmt.Errorf("MCP[%s]: failed to start transport: %w", c.name, err)
	}

	// Initialize
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "openuai",
		Version: "0.1.0",
	}
	initReq.Params.Capabilities = mcp.ClientCapabilities{}

	_, err := c.client.Initialize(ctx, initReq)
	if err != nil {
		c.client.Close()
		return fmt.Errorf("MCP[%s]: initialize failed: %w", c.name, err)
	}
	logger.Info("MCP[%s]: initialized", c.name)

	// Discover tools
	toolsResult, err := c.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		logger.Error("MCP[%s]: failed to list tools: %s", c.name, err.Error())
	} else {
		c.tools = toolsResult.Tools
		logger.Info("MCP[%s]: discovered %d tools", c.name, len(c.tools))
	}

	// Discover resources
	caps := c.client.GetServerCapabilities()
	if caps.Resources != nil {
		resResult, err := c.client.ListResources(ctx, mcp.ListResourcesRequest{})
		if err != nil {
			logger.Error("MCP[%s]: failed to list resources: %s", c.name, err.Error())
		} else {
			c.resources = resResult.Resources
			logger.Info("MCP[%s]: discovered %d resources", c.name, len(c.resources))
		}

		// Subscribe to requested URIs
		if caps.Resources.Subscribe {
			for _, uri := range c.subscribeURIs {
				subReq := mcp.SubscribeRequest{}
				subReq.Params.URI = uri
				if err := c.client.Subscribe(ctx, subReq); err != nil {
					logger.Info("MCP[%s]: subscribe to %s not supported (notifications still work): %s", c.name, uri, err.Error())
				} else {
					logger.Info("MCP[%s]: subscribed to %s", c.name, uri)
				}
			}
		}
	}

	// Listen for notifications — handle in a separate goroutine to avoid
	// blocking the client's reader (which would deadlock ReadResource calls).
	c.client.OnNotification(func(notification mcp.JSONRPCNotification) {
		if notification.Method == string(mcp.MethodNotificationResourceUpdated) {
			if uri, ok := notification.Params.AdditionalFields["uri"].(string); ok {
				logger.Info("MCP[%s]: resource updated: %s", c.name, uri)
				if c.onResourceUpdated != nil {
					go c.onResourceUpdated(c, uri)
				}
			}
		}
	})

	return nil
}

// Stop shuts down the MCP server connection.
func (c *Connection) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		logger.Info("MCP[%s]: stopping", c.name)
		return c.client.Close()
	}
	return nil
}

// Tools returns the discovered MCP tools.
func (c *Connection) Tools() []mcp.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tools
}

// Resources returns the discovered MCP resources.
func (c *Connection) Resources() []mcp.Resource {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resources
}

// CallTool invokes a tool on the remote MCP server.
func (c *Connection) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	c.mu.Lock()
	cl := c.client
	c.mu.Unlock()
	if cl == nil {
		return nil, fmt.Errorf("MCP[%s]: not connected", c.name)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	return cl.CallTool(ctx, req)
}

// ReadResource reads a resource from the remote MCP server.
func (c *Connection) ReadResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, error) {
	c.mu.Lock()
	cl := c.client
	c.mu.Unlock()
	if cl == nil {
		return nil, fmt.Errorf("MCP[%s]: not connected", c.name)
	}

	req := mcp.ReadResourceRequest{}
	req.Params.URI = uri
	return cl.ReadResource(ctx, req)
}
