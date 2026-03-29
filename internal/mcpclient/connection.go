package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"openuai/internal/config"
	"openuai/internal/logger"
)

// Connection manages a single connection to an MCP server (STDIO or HTTP).
type Connection struct {
	name string
	cfg  config.MCPServerConfig

	// STDIO fields
	env []string

	client *client.Client
	mu     sync.Mutex

	tools     []mcp.Tool
	resources []mcp.Resource

	// process is the launched subprocess for HTTP servers that need auto-start.
	process *exec.Cmd

	// subscribeURIs are the resource URIs to subscribe to for notifications.
	subscribeURIs []string

	// onResourceUpdated is called when a subscribed resource is updated.
	onResourceUpdated func(conn *Connection, uri string)

	// tokenStore persists OAuth tokens for HTTP connections.
	tokenStore *FileTokenStore

	// needsAuth is true when the server returned 401 but we haven't run the OAuth flow yet.
	needsAuth bool

	// oauthHandler is stored when auth is needed so we can run the flow later on demand.
	pendingOAuthHandler *transport.OAuthHandler

	// onTokensSaved is called when OAuth tokens are saved, so the caller can persist to config.
	onTokensSaved func(name string, tokens *config.MCPOAuthTokens)
}

// NewConnection creates a new MCP server connection.
func NewConnection(name string, cfg config.MCPServerConfig) *Connection {
	envSlice := make([]string, 0, len(cfg.Env))
	for k, v := range cfg.Env {
		envSlice = append(envSlice, k+"="+v)
	}
	return &Connection{
		name:          name,
		cfg:           cfg,
		env:           envSlice,
		subscribeURIs: cfg.Subscribe,
	}
}

// Name returns the connection name.
func (c *Connection) Name() string { return c.name }

// IsHTTP returns true if this is an HTTP connection.
func (c *Connection) IsHTTP() bool { return c.cfg.IsHTTP() }

// Start launches the MCP server, initializes the connection,
// discovers tools and resources, and subscribes to resource updates.
func (c *Connection) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cfg.IsHTTP() {
		return c.startHTTP(ctx)
	}
	return c.startStdio(ctx)
}

// startStdio launches a subprocess-based MCP server.
func (c *Connection) startStdio(ctx context.Context) error {
	logger.Info("MCP[%s]: starting server %s %v", c.name, c.cfg.Command, c.cfg.Args)

	stdioTransport := transport.NewStdio(c.cfg.Command, c.env, c.cfg.Args...)
	c.client = client.NewClient(stdioTransport)

	if err := c.client.Start(ctx); err != nil {
		return fmt.Errorf("MCP[%s]: failed to start transport: %w", c.name, err)
	}

	return c.initialize(ctx)
}

// startHTTP connects to a remote HTTP MCP server, handling OAuth if needed.
func (c *Connection) startHTTP(ctx context.Context) error {
	// If the server needs launching first (command + URL), start the process
	if c.cfg.NeedsLaunch() {
		logger.Info("MCP[%s]: launching server %s %v", c.name, c.cfg.Command, c.cfg.Args)
		c.process = exec.Command(c.cfg.Command, c.cfg.Args...)
		c.process.Env = append(c.process.Environ(), c.env...)
		if err := c.process.Start(); err != nil {
			return fmt.Errorf("MCP[%s]: failed to launch server: %w", c.name, err)
		}
		// Wait for the HTTP server to be ready
		if err := c.waitForHTTP(ctx); err != nil {
			c.process.Process.Kill()
			return fmt.Errorf("MCP[%s]: server did not become ready: %w", c.name, err)
		}
	}

	logger.Info("MCP[%s]: connecting to HTTP server %s", c.name, c.cfg.URL)

	// Build token store that persists to config
	c.tokenStore = NewFileTokenStore(c.name)

	// Restore saved tokens if available
	if c.cfg.OAuthTokens != nil {
		c.tokenStore.Restore(c.cfg.OAuthTokens)
		logger.Info("MCP[%s]: restored saved OAuth tokens", c.name)
	}

	opts := []transport.StreamableHTTPCOption{
		transport.WithContinuousListening(),
	}

	// Always configure OAuth — the server will tell us if it's needed via 401
	oauthCfg := transport.OAuthConfig{
		RedirectURI: "http://localhost:19120/oauth/callback",
		PKCEEnabled: true,
		TokenStore:  c.tokenStore,
	}
	// Restore client credentials if we registered before
	if c.cfg.OAuthTokens != nil {
		oauthCfg.ClientID = c.cfg.OAuthTokens.ClientID
		oauthCfg.ClientSecret = c.cfg.OAuthTokens.ClientSecret
	}
	opts = append(opts, transport.WithHTTPOAuth(oauthCfg))

	// Add custom headers if any
	if len(c.cfg.Env) > 0 {
		headers := make(map[string]string)
		for k, v := range c.cfg.Env {
			headers[k] = v
		}
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}

	httpTransport, err := transport.NewStreamableHTTP(c.cfg.URL, opts...)
	if err != nil {
		return fmt.Errorf("MCP[%s]: failed to create HTTP transport: %w", c.name, err)
	}

	c.client = client.NewClient(httpTransport)

	if err := c.client.Start(ctx); err != nil {
		if !c.handleOAuthIfNeeded(ctx, err) {
			return fmt.Errorf("MCP[%s]: failed to start HTTP transport: %w", c.name, err)
		}
		return c.retryHTTPAfterAuth(ctx)
	}

	if err := c.initialize(ctx); err != nil {
		if !c.handleOAuthIfNeeded(ctx, err) {
			return err
		}
		return c.retryHTTPAfterAuth(ctx)
	}

	// Save tokens after successful connection
	c.persistTokens()

	return nil
}

// handleOAuthIfNeeded checks if the error is an OAuth auth required error.
// If tokens are already saved, runs the flow automatically (token refresh).
// Otherwise, marks the connection as needing auth so the user can trigger it manually.
func (c *Connection) handleOAuthIfNeeded(ctx context.Context, err error) bool {
	var authErr *transport.OAuthAuthorizationRequiredError
	if !errors.As(err, &authErr) {
		return false
	}

	// If we had saved tokens, try refresh automatically (no user interaction needed)
	if c.cfg.OAuthTokens != nil && c.cfg.OAuthTokens.AccessToken != "" {
		logger.Info("MCP[%s]: OAuth token expired, attempting refresh", c.name)
		if flowErr := RunOAuthFlow(ctx, authErr.Handler); flowErr != nil {
			logger.Error("MCP[%s]: OAuth refresh failed: %s", c.name, flowErr)
			// Fall through to manual auth
		} else {
			return true
		}
	}

	// No saved tokens or refresh failed — wait for user to trigger auth
	logger.Info("MCP[%s]: OAuth authorization required — waiting for user to authenticate", c.name)
	c.needsAuth = true
	c.pendingOAuthHandler = authErr.Handler
	return false
}

// NeedsAuth returns true if this connection is waiting for the user to authenticate.
func (c *Connection) NeedsAuth() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.needsAuth
}

// RunPendingAuth executes the OAuth flow that was deferred, then reconnects.
func (c *Connection) RunPendingAuth(ctx context.Context) error {
	c.mu.Lock()
	handler := c.pendingOAuthHandler
	c.mu.Unlock()

	if handler == nil {
		return fmt.Errorf("no pending auth")
	}

	logger.Info("MCP[%s]: user triggered OAuth flow", c.name)
	if err := RunOAuthFlow(ctx, handler); err != nil {
		return fmt.Errorf("OAuth flow failed: %w", err)
	}

	c.mu.Lock()
	c.needsAuth = false
	c.pendingOAuthHandler = nil
	c.mu.Unlock()

	// Reconnect
	return c.retryHTTPAfterAuth(ctx)
}

// retryHTTPAfterAuth recreates the HTTP transport and retries initialization.
func (c *Connection) retryHTTPAfterAuth(ctx context.Context) error {
	logger.Info("MCP[%s]: retrying HTTP connection after OAuth", c.name)

	opts := []transport.StreamableHTTPCOption{
		transport.WithContinuousListening(),
	}
	oauthCfg := transport.OAuthConfig{
		RedirectURI: "http://localhost:19120/oauth/callback",
		PKCEEnabled: true,
		TokenStore:  c.tokenStore,
	}
	if c.cfg.OAuthTokens != nil {
		oauthCfg.ClientID = c.cfg.OAuthTokens.ClientID
		oauthCfg.ClientSecret = c.cfg.OAuthTokens.ClientSecret
	}
	opts = append(opts, transport.WithHTTPOAuth(oauthCfg))

	httpTransport, err := transport.NewStreamableHTTP(c.cfg.URL, opts...)
	if err != nil {
		return fmt.Errorf("MCP[%s]: failed to recreate HTTP transport: %w", c.name, err)
	}

	c.client = client.NewClient(httpTransport)
	if err := c.client.Start(ctx); err != nil {
		return fmt.Errorf("MCP[%s]: failed to start HTTP transport after auth: %w", c.name, err)
	}

	if err := c.initialize(ctx); err != nil {
		return err
	}

	c.persistTokens()
	return nil
}

// persistTokens saves current OAuth tokens to config via callback.
func (c *Connection) persistTokens() {
	if c.tokenStore == nil || c.onTokensSaved == nil {
		return
	}
	tok, err := c.tokenStore.GetToken(context.Background())
	if err != nil {
		return
	}
	c.onTokensSaved(c.name, &config.MCPOAuthTokens{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
		ExpiresAt:    tok.ExpiresAt.Unix(),
		ClientID:     c.tokenStore.clientID,
		ClientSecret: c.tokenStore.clientSecret,
	})
}

// initialize performs MCP handshake, discovers tools/resources, and sets up notifications.
func (c *Connection) initialize(ctx context.Context) error {
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
					logger.Info("MCP[%s]: subscribe to %s not supported: %s", c.name, uri, err.Error())
				} else {
					logger.Info("MCP[%s]: subscribed to %s", c.name, uri)
				}
			}
		}
	}

	// Listen for notifications
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

// waitForHTTP polls the server URL until it responds or the context is cancelled.
func (c *Connection) waitForHTTP(ctx context.Context) error {
	deadline := time.After(15 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	httpClient := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for HTTP server at %s", c.cfg.URL)
		case <-ticker.C:
			resp, err := httpClient.Get(c.cfg.URL)
			if err == nil {
				resp.Body.Close()
				logger.Info("MCP[%s]: HTTP server ready", c.name)
				return nil
			}
		}
	}
}

// Stop shuts down the MCP server connection.
func (c *Connection) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		logger.Info("MCP[%s]: stopping", c.name)
		c.client.Close()
	}
	if c.process != nil && c.process.Process != nil {
		logger.Info("MCP[%s]: killing launched process", c.name)
		c.process.Process.Kill()
		c.process = nil
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

// FileTokenStore implements transport.TokenStore, persisting tokens in memory
// with the ability to save/restore from config.
type FileTokenStore struct {
	name         string
	token        *transport.Token
	clientID     string
	clientSecret string
	mu           sync.RWMutex
}

// NewFileTokenStore creates a token store for the given server name.
func NewFileTokenStore(name string) *FileTokenStore {
	return &FileTokenStore{name: name}
}

// Restore loads tokens from saved config.
func (s *FileTokenStore) Restore(saved *config.MCPOAuthTokens) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clientID = saved.ClientID
	s.clientSecret = saved.ClientSecret
	if saved.AccessToken != "" {
		s.token = &transport.Token{
			AccessToken:  saved.AccessToken,
			RefreshToken: saved.RefreshToken,
			TokenType:    saved.TokenType,
			ExpiresAt:    time.Unix(saved.ExpiresAt, 0),
		}
	}
}

// GetToken returns the current token.
func (s *FileTokenStore) GetToken(ctx context.Context) (*transport.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.token == nil {
		return nil, transport.ErrNoToken
	}
	return s.token, nil
}

// SaveToken saves a token.
func (s *FileTokenStore) SaveToken(ctx context.Context, token *transport.Token) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
	return nil
}
