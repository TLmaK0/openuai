// mcp-echo is a minimal MCP server for testing the OpenUAI MCP client.
// It provides:
// - Tool "echo": echoes back the input text
// - Tool "inject_message": simulates an incoming message (triggers resource notification)
// - Resource "echo://messages/inbox": returns recent injected messages
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type message struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	FromName  string `json:"from_name"`
	Body      string `json:"body"`
	Timestamp int64  `json:"timestamp"`
	IsGroup   bool   `json:"is_group"`
	GroupName string `json:"group_name"`
}

var (
	inbox   []message
	inboxMu sync.RWMutex
	counter int
)

const resourceURI = "echo://messages/inbox"

func main() {
	mcpServer := server.NewMCPServer("mcp-echo", "0.1.0",
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(true, false),
	)

	// Tool: echo
	mcpServer.AddTool(mcp.Tool{
		Name:        "echo",
		Description: "Echoes back the input text",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Text to echo back",
				},
			},
			Required: []string{"text"},
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.Params.Arguments.(map[string]any)
		text, _ := args["text"].(string)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "Echo: " + text}},
		}, nil
	})

	// Tool: inject_message — simulates an incoming message
	mcpServer.AddTool(mcp.Tool{
		Name:        "inject_message",
		Description: "Inject a fake incoming message (for testing events)",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"from": map[string]any{
					"type":        "string",
					"description": "Sender identifier",
				},
				"from_name": map[string]any{
					"type":        "string",
					"description": "Sender display name",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Message body",
				},
			},
			Required: []string{"body"},
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.Params.Arguments.(map[string]any)
		body, _ := args["body"].(string)
		from, _ := args["from"].(string)
		fromName, _ := args["from_name"].(string)
		if from == "" {
			from = "test@echo"
		}
		if fromName == "" {
			fromName = "Test User"
		}

		inboxMu.Lock()
		counter++
		msg := message{
			ID:        fmt.Sprintf("echo_%d", counter),
			From:      from,
			FromName:  fromName,
			Body:      body,
			Timestamp: time.Now().Unix(),
		}
		inbox = append(inbox, msg)
		if len(inbox) > 100 {
			inbox = inbox[len(inbox)-100:]
		}
		inboxMu.Unlock()

		// Notify clients that the resource was updated
		mcpServer.SendNotificationToAllClients(
			string(mcp.MethodNotificationResourceUpdated),
			map[string]any{"uri": resourceURI},
		)

		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Type: "text", Text: fmt.Sprintf("Injected message %s", msg.ID)}},
		}, nil
	})

	// Resource: inbox
	mcpServer.AddResource(
		mcp.NewResource(
			resourceURI,
			"Echo Inbox",
			mcp.WithResourceDescription("Simulated message inbox for testing"),
			mcp.WithMIMEType("application/json"),
		),
		func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			inboxMu.RLock()
			data, _ := json.Marshal(inbox)
			inboxMu.RUnlock()
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      resourceURI,
					MIMEType: "application/json",
					Text:     string(data),
				},
			}, nil
		},
	)

	log.Println("mcp-echo: starting stdio server")
	if err := server.ServeStdio(mcpServer); err != nil {
		log.Fatal("mcp-echo: stdio error:", err)
	}
}
