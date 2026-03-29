package mcpclient

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"openuai/internal/config"
	"openuai/internal/eventbus"
)

// TestIntegrationEchoServer tests the MCP client against the real mcp-echo server.
// It builds the echo server binary, starts a connection, calls tools, and tests notifications.
func TestIntegrationEchoServer(t *testing.T) {
	// Build echo server
	binPath := t.TempDir() + "/mcp-echo"
	cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/mcp-echo")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to build mcp-echo: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Create connection
	conn := NewConnection("echo-test", config.MCPServerConfig{
		Name:      "echo-test",
		Command:   binPath,
		Subscribe: []string{"echo://messages/inbox"},
	})

	// Track resource update notifications
	notifCh := make(chan string, 10)
	conn.onResourceUpdated = func(c *Connection, uri string) {
		notifCh <- uri
	}

	// Start connection
	if err := conn.Start(ctx); err != nil {
		t.Fatalf("Failed to start connection: %v", err)
	}
	defer conn.Stop()

	// Verify tools were discovered
	tools := conn.Tools()
	if len(tools) < 2 {
		t.Fatalf("Expected at least 2 tools (echo, inject_message), got %d", len(tools))
	}

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}
	if !toolNames["echo"] {
		t.Error("Expected 'echo' tool")
	}
	if !toolNames["inject_message"] {
		t.Error("Expected 'inject_message' tool")
	}

	// Verify resources were discovered
	resources := conn.Resources()
	if len(resources) < 1 {
		t.Fatalf("Expected at least 1 resource, got %d", len(resources))
	}
	if resources[0].URI != "echo://messages/inbox" {
		t.Errorf("Expected resource URI 'echo://messages/inbox', got %q", resources[0].URI)
	}

	// Test echo tool
	result, err := conn.CallTool(ctx, "echo", map[string]any{"text": "hello world"})
	if err != nil {
		t.Fatalf("CallTool echo failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("echo tool returned error")
	}
	if len(result.Content) == 0 {
		t.Fatal("echo tool returned empty content")
	}

	// Test inject_message tool (should trigger notification)
	result, err = conn.CallTool(ctx, "inject_message", map[string]any{
		"body":      "test message",
		"from":      "tester@test",
		"from_name": "Tester",
	})
	if err != nil {
		t.Fatalf("CallTool inject_message failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("inject_message tool returned error")
	}

	// Wait for notification
	select {
	case uri := <-notifCh:
		if uri != "echo://messages/inbox" {
			t.Errorf("Expected notification for 'echo://messages/inbox', got %q", uri)
		}
	case <-time.After(5 * time.Second):
		t.Error("Timed out waiting for resource update notification")
	}

	// Read the resource and verify the message is there
	resResult, err := conn.ReadResource(ctx, "echo://messages/inbox")
	if err != nil {
		t.Fatalf("ReadResource failed: %v", err)
	}
	if len(resResult.Contents) == 0 {
		t.Fatal("ReadResource returned empty contents")
	}

	t.Logf("Integration test passed: tools=%d, resources=%d", len(tools), len(resources))
}

// TestIntegrationManagerWithEcho tests the full manager flow with event publishing.
func TestIntegrationManagerWithEcho(t *testing.T) {
	// Build echo server
	binPath := t.TempDir() + "/mcp-echo"
	cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/mcp-echo")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to build mcp-echo: %v", err)
	}

	events := make(chan eventbus.Event, 10)

	mgr := NewManager(nil)
	mgr.eventCh = events

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Manually start a connection through the manager
	conn := NewConnection("echo", config.MCPServerConfig{
		Name:      "echo",
		Command:   binPath,
		Subscribe: []string{"echo://messages/inbox"},
	})
	conn.onResourceUpdated = mgr.handleResourceUpdated

	if err := conn.Start(ctx); err != nil {
		t.Fatalf("Failed to start connection: %v", err)
	}
	defer conn.Stop()

	mgr.mu.Lock()
	mgr.connections["echo"] = conn
	mgr.mu.Unlock()

	// Track notifications and let the manager handle them
	notifCh := make(chan string, 10)
	conn.onResourceUpdated = func(c *Connection, uri string) {
		notifCh <- uri
		mgr.handleResourceUpdated(c, uri)
	}

	// Inject a seed message to initialize the cursor (first read seeds, no events emitted)
	_, err := conn.CallTool(ctx, "inject_message", map[string]any{
		"body":      "seed message",
		"from":      "seed@test",
		"from_name": "Seed",
	})
	if err != nil {
		t.Fatalf("inject_message (seed) failed: %v", err)
	}

	// Wait for seed notification
	select {
	case <-notifCh:
		// Seed processed — cursor is now set
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for seed notification")
	}

	// Wait so the next message gets a strictly greater Unix timestamp
	time.Sleep(1100 * time.Millisecond)

	// Inject the real message (should now emit an event)
	_, err = conn.CallTool(ctx, "inject_message", map[string]any{
		"body":      "hello from integration test",
		"from":      "5491155551234@s.whatsapp.net",
		"from_name": "Test User",
	})
	if err != nil {
		t.Fatalf("inject_message failed: %v", err)
	}

	// Wait for notification to arrive
	select {
	case uri := <-notifCh:
		t.Logf("Got notification for %s", uri)
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for notification — notification mechanism may not be working")
	}

	// Now check for the event
	select {
	case evt := <-events:
		if evt.Source != "echo" {
			t.Errorf("Expected source 'echo', got %q", evt.Source)
		}
		if evt.Payload != "hello from integration test" {
			t.Errorf("Expected payload 'hello from integration test', got %q", evt.Payload)
		}
		if evt.Type != eventbus.EventMessage {
			t.Errorf("Expected type 'message', got %q", evt.Type)
		}
		if evt.Metadata["sender"] != "5491155551234@s.whatsapp.net" {
			t.Errorf("Expected sender '5491155551234@s.whatsapp.net', got %q", evt.Metadata["sender"])
		}
		t.Logf("Event received: source=%s payload=%s", evt.Source, evt.Payload)
	case <-time.After(5 * time.Second):
		t.Error("Timed out waiting for event from manager")
	}

	// Verify MCP tools are accessible
	allTools := mgr.AllTools()
	if len(allTools) < 2 {
		t.Errorf("Expected at least 2 MCP tools, got %d", len(allTools))
	}
	if _, ok := allTools["mcp_echo_echo"]; !ok {
		t.Error("Expected tool 'mcp_echo_echo'")
	}
	if _, ok := allTools["mcp_echo_inject_message"]; !ok {
		t.Error("Expected tool 'mcp_echo_inject_message'")
	}
}
