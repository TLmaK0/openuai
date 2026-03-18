package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"openuai/internal/config"
	"openuai/internal/eventbus"
	"openuai/internal/logger"
)

// Manager manages multiple MCP server connections and bridges events to the event bus.
// It implements eventbus.EventSource.
type Manager struct {
	connections map[string]*Connection
	configs     []config.MCPServerConfig
	mu          sync.RWMutex
	eventCh     chan<- eventbus.Event
	cancel      context.CancelFunc

	// lastSeenTime tracks the Unix timestamp of the last published message per URI.
	// On first load we seed it to "now" to skip historical messages.
	lastSeenTime map[string]int64

	// seeded tracks whether the cursor has been initialized for a URI.
	seeded map[string]bool

	// onReady is called after all auto-start servers have initialized.
	// Used to register MCP tools in the agent's tool registry.
	onReady func()
}

// NewManager creates a new MCP manager from config.
func NewManager(configs []config.MCPServerConfig) *Manager {
	return &Manager{
		connections:  make(map[string]*Connection),
		configs:      configs,
		lastSeenTime: make(map[string]int64),
		seeded:       make(map[string]bool),
	}
}

// Name implements eventbus.EventSource.
func (m *Manager) Name() string { return "mcp" }

// OnReady sets a callback that fires after all auto-start servers have initialized.
func (m *Manager) OnReady(fn func()) {
	m.onReady = fn
}

// Start implements eventbus.EventSource. It launches all configured MCP servers.
func (m *Manager) Start(events chan<- eventbus.Event) error {
	m.eventCh = events
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	for _, cfg := range m.configs {
		if !cfg.AutoStart {
			continue
		}
		if err := m.startServer(ctx, cfg); err != nil {
			logger.Error("MCP manager: failed to start %s: %s", cfg.Name, err.Error())
		}
	}

	// Signal that initial connections are ready
	if m.onReady != nil {
		m.onReady()
	}

	// Block until cancelled
	<-ctx.Done()
	return nil
}

// Stop implements eventbus.EventSource.
func (m *Manager) Stop() error {
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, conn := range m.connections {
		if err := conn.Stop(); err != nil {
			logger.Error("MCP manager: error stopping %s: %s", name, err.Error())
		}
	}
	m.connections = make(map[string]*Connection)
	return nil
}

// startServer launches a single MCP server connection.
func (m *Manager) startServer(ctx context.Context, cfg config.MCPServerConfig) error {
	conn := NewConnection(cfg.Name, cfg.Command, cfg.Args, cfg.Env, cfg.Subscribe)
	conn.onResourceUpdated = m.handleResourceUpdated

	if err := conn.Start(ctx); err != nil {
		return err
	}

	m.mu.Lock()
	m.connections[cfg.Name] = conn
	m.mu.Unlock()

	logger.Info("MCP manager: server %s started", cfg.Name)

	// Start reconnection watcher
	go m.watchConnection(ctx, cfg, conn)

	return nil
}

// watchConnection monitors a connection and reconnects with backoff on failure.
func (m *Manager) watchConnection(ctx context.Context, cfg config.MCPServerConfig, conn *Connection) {
	// Simple ping loop to detect disconnection
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	backoff := time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Check if still alive by trying a ping
			conn.mu.Lock()
			cl := conn.client
			conn.mu.Unlock()
			if cl == nil {
				// Reconnect
				logger.Info("MCP manager: reconnecting %s (backoff %v)", cfg.Name, backoff)
				time.Sleep(backoff)
				if err := m.startServer(ctx, cfg); err != nil {
					logger.Error("MCP manager: reconnect %s failed: %s", cfg.Name, err.Error())
					backoff = min(backoff*2, maxBackoff)
				} else {
					backoff = time.Second
				}
				return // New watchConnection goroutine will be spawned
			}
		}
	}
}

// handleResourceUpdated is called when a subscribed resource is updated.
// It reads the resource, deduplicates messages, and publishes events.
func (m *Manager) handleResourceUpdated(conn *Connection, uri string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := conn.ReadResource(ctx, uri)
	if err != nil {
		logger.Error("MCP manager: failed to read resource %s from %s: %s", uri, conn.Name(), err.Error())
		return
	}

	if len(result.Contents) == 0 {
		logger.Error("MCP manager: empty contents from %s resource %s", conn.Name(), uri)
		return
	}

	for _, content := range result.Contents {
		switch tc := content.(type) {
		case mcp.TextResourceContents:
			if tc.Text != "" {
				m.processResourceContent(conn.Name(), uri, tc.Text)
			}
		default:
			// Try JSON marshal/unmarshal for unknown types
			data, err := json.Marshal(content)
			if err == nil {
				var trc mcp.TextResourceContents
				if json.Unmarshal(data, &trc) == nil && trc.Text != "" {
					m.processResourceContent(conn.Name(), uri, trc.Text)
				}
			}
		}
	}
}

// MCPMessage is the expected JSON structure from message resources.
// Supports multiple formats: native OpenUAI format and lharries/whatsapp-mcp format.
type MCPMessage struct {
	// Common fields
	ID string `json:"id"`

	// Native format
	From      string `json:"from"`
	FromName  string `json:"from_name"`
	Body      string `json:"body"`
	Timestamp any    `json:"timestamp"` // int64 (unix) or string (ISO-8601)
	IsGroup   bool   `json:"is_group"`
	GroupName string `json:"group_name"`

	// lharries/whatsapp-mcp format
	Sender    string `json:"sender"`
	ChatName  string `json:"chat_name"`
	Content   string `json:"content"`
	ChatJID   string `json:"chat_jid"`
	IsFromMe  bool   `json:"is_from_me"`
	MediaType string `json:"media_type"`
}

// GetSender returns the sender, trying native format first, then lharries format.
func (m MCPMessage) GetSender() string {
	if m.From != "" {
		return m.From
	}
	return m.Sender
}

// GetSenderName returns the display name of the sender.
func (m MCPMessage) GetSenderName() string {
	if m.FromName != "" {
		return m.FromName
	}
	return m.ChatName
}

// GetBody returns the message body/content.
func (m MCPMessage) GetBody() string {
	if m.Body != "" {
		return m.Body
	}
	return m.Content
}

// GetTimestamp returns the Unix timestamp, parsing from either int or ISO string.
func (m MCPMessage) GetTimestamp() int64 {
	switch v := m.Timestamp.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case string:
		formats := []string{
			time.RFC3339,
			"2006-01-02 15:04:05-07:00",
			"2006-01-02 15:04:05",
		}
		for _, f := range formats {
			if t, err := time.Parse(f, v); err == nil {
				return t.Unix()
			}
		}
		return 0
	}
	return 0
}

// processResourceContent parses a JSON array of messages, deduplicates by timestamp, and publishes events.
// Uses a timestamp cursor so it works correctly even when messages slide out of the fetch window.
func (m *Manager) processResourceContent(serverName, uri, text string) {
	var messages []MCPMessage
	if err := json.Unmarshal([]byte(text), &messages); err != nil {
		logger.Error("MCP manager: failed to parse resource content from %s: %s", serverName, err.Error())
		return
	}
	if len(messages) == 0 {
		return
	}

	m.mu.Lock()
	seeded := m.seeded[uri]
	lastTime := m.lastSeenTime[uri]
	m.mu.Unlock()

	// On first load: seed the cursor to the newest message timestamp, skip all history.
	if !seeded {
		newest := int64(0)
		for _, msg := range messages {
			if t := msg.GetTimestamp(); t > newest {
				newest = t
			}
		}
		m.mu.Lock()
		m.lastSeenTime[uri] = newest
		m.seeded[uri] = true
		m.mu.Unlock()
		logger.Info("MCP manager: %s first load, seeding cursor at t=%d (skipping %d historical messages)", serverName, newest, len(messages))
		return
	}

	// Collect messages strictly newer than the cursor.
	var newMessages []MCPMessage
	newestTime := lastTime
	for _, msg := range messages {
		t := msg.GetTimestamp()
		if t > lastTime {
			newMessages = append(newMessages, msg)
			if t > newestTime {
				newestTime = t
			}
		}
	}

	if len(newMessages) == 0 {
		return
	}

	// Advance cursor to the newest timestamp seen.
	m.mu.Lock()
	m.lastSeenTime[uri] = newestTime
	m.mu.Unlock()

	// Publish events
	for _, msg := range newMessages {
		metadata := map[string]string{
			"sender":      msg.GetSender(),
			"sender_name": msg.GetSenderName(),
			"message_id":  msg.ID,
		}
		if msg.IsGroup {
			metadata["is_group"] = "true"
			metadata["group_name"] = msg.GroupName
		}
		if msg.ChatJID != "" {
			metadata["chat_jid"] = msg.ChatJID
		}
		if msg.IsFromMe {
			metadata["is_from_me"] = "true"
		}
		if msg.MediaType != "" {
			metadata["media_type"] = msg.MediaType
		}

		evt := eventbus.Event{
			ID:        fmt.Sprintf("mcp_%s_%s", serverName, msg.ID),
			Source:    serverName,
			Type:      eventbus.EventMessage,
			Payload:   msg.GetBody(),
			Metadata:  metadata,
			Timestamp: time.Unix(msg.GetTimestamp(), 0),
		}

		if m.eventCh != nil {
			m.eventCh <- evt
		}
	}

	logger.Info("MCP manager: published %d new messages from %s", len(newMessages), serverName)
}

// GetConnections returns info about all active connections.
func (m *Manager) GetConnections() []ConnectionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	infos := make([]ConnectionInfo, 0, len(m.connections))
	for name, conn := range m.connections {
		infos = append(infos, ConnectionInfo{
			Name:      name,
			Tools:     len(conn.Tools()),
			Resources: len(conn.Resources()),
			Connected: conn.client != nil,
		})
	}
	return infos
}

// ConnectionInfo holds status information about an MCP connection.
type ConnectionInfo struct {
	Name      string `json:"name"`
	Tools     int    `json:"tools"`
	Resources int    `json:"resources"`
	Connected bool   `json:"connected"`
}

// GetConnection returns a specific connection by name.
func (m *Manager) GetConnection(name string) (*Connection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conn, ok := m.connections[name]
	return conn, ok
}

// CallTool calls a tool on the specified MCP server.
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*mcp.CallToolResult, error) {
	conn, ok := m.GetConnection(serverName)
	if !ok {
		return nil, fmt.Errorf("MCP server %q not found", serverName)
	}
	return conn.CallTool(ctx, toolName, args)
}

// AllTools returns all tools from all connections, keyed by "serverName.toolName".
func (m *Manager) AllTools() map[string]MCPToolRef {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]MCPToolRef)
	for name, conn := range m.connections {
		for _, tool := range conn.Tools() {
			key := fmt.Sprintf("mcp_%s_%s", name, tool.Name)
			result[key] = MCPToolRef{
				ServerName: name,
				Tool:       tool,
			}
		}
	}
	return result
}

// MCPToolRef references an MCP tool on a specific server.
type MCPToolRef struct {
	ServerName string
	Tool       mcp.Tool
}
