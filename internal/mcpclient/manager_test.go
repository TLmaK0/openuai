package mcpclient

import (
	"encoding/json"
	"testing"

	"openuai/internal/config"
	"openuai/internal/eventbus"
)

func TestNewManager(t *testing.T) {
	configs := []config.MCPServerConfig{
		{Name: "test", Command: "echo", AutoStart: false},
	}
	m := NewManager(configs)
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
	if m.Name() != "mcp" {
		t.Errorf("expected name 'mcp', got %q", m.Name())
	}
}

func TestProcessResourceContent(t *testing.T) {
	m := NewManager(nil)
	events := make(chan eventbus.Event, 10)
	m.eventCh = events

	messages := []MCPMessage{
		{ID: "1", From: "sender1", FromName: "Alice", Body: "Hello", Timestamp: 1000},
		{ID: "2", From: "sender2", FromName: "Bob", Body: "World", Timestamp: 1001, IsGroup: true, GroupName: "Test Group"},
	}
	data, _ := json.Marshal(messages)

	m.processResourceContent("whatsapp", "whatsapp://messages/inbox", string(data))

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	evt1 := <-events
	if evt1.Source != "whatsapp" {
		t.Errorf("expected source 'whatsapp', got %q", evt1.Source)
	}
	if evt1.Payload != "Hello" {
		t.Errorf("expected payload 'Hello', got %q", evt1.Payload)
	}
	if evt1.Type != eventbus.EventMessage {
		t.Errorf("expected type 'message', got %q", evt1.Type)
	}
	if evt1.Metadata["sender"] != "sender1" {
		t.Errorf("expected sender 'sender1', got %q", evt1.Metadata["sender"])
	}

	evt2 := <-events
	if evt2.Metadata["is_group"] != "true" {
		t.Errorf("expected is_group 'true', got %q", evt2.Metadata["is_group"])
	}
	if evt2.Metadata["group_name"] != "Test Group" {
		t.Errorf("expected group_name 'Test Group', got %q", evt2.Metadata["group_name"])
	}
}

func TestProcessResourceContentDedup(t *testing.T) {
	m := NewManager(nil)
	events := make(chan eventbus.Event, 10)
	m.eventCh = events

	messages1 := []MCPMessage{
		{ID: "1", From: "a", Body: "first", Timestamp: 1000},
	}
	data1, _ := json.Marshal(messages1)
	m.processResourceContent("wa", "whatsapp://inbox", string(data1))

	// Drain first event
	<-events

	// Second batch includes old + new
	messages2 := []MCPMessage{
		{ID: "1", From: "a", Body: "first", Timestamp: 1000},
		{ID: "2", From: "b", Body: "second", Timestamp: 1001},
	}
	data2, _ := json.Marshal(messages2)
	m.processResourceContent("wa", "whatsapp://inbox", string(data2))

	if len(events) != 1 {
		t.Fatalf("expected 1 new event (deduped), got %d", len(events))
	}

	evt := <-events
	if evt.Payload != "second" {
		t.Errorf("expected payload 'second', got %q", evt.Payload)
	}
}

func TestGetConnections_Empty(t *testing.T) {
	m := NewManager(nil)
	conns := m.GetConnections()
	if len(conns) != 0 {
		t.Errorf("expected 0 connections, got %d", len(conns))
	}
}
