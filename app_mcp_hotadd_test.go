package main

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"openuai/internal/agent"
	"openuai/internal/config"
	"openuai/internal/mcpclient"
	"openuai/internal/tools"
)

// Issue #93: with no MCP servers at startup, one added afterwards (Settings
// or marketplace, both go through AddMCPServer) connects at once, its tools
// reach the assistant, and removing the last one disconnects it.
func TestAddMCPServerWithNoneConfigured(t *testing.T) {
	bin := t.TempDir() + "/mcp-echo"
	if out, err := exec.Command("go", "build", "-o", bin, "./cmd/mcp-echo").CombinedOutput(); err != nil {
		t.Fatalf("build mcp-echo: %v\n%s", err, out)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers()) != 0 {
		t.Fatal("test needs a config with no MCP servers")
	}
	a := &App{cfg: cfg, registry: tools.NewRegistry(), recentSentIDs: map[string]int64{}}
	// As startup does, now also with no servers configured.
	a.mcpManager = mcpclient.NewManager(cfg.MCPServers())
	defer a.mcpManager.Stop()
	a.currentAgent = &agent.Agent{}

	if msg := a.AddMCPServer("echo", bin, nil, nil, true, nil, ""); msg != "" {
		t.Fatal(msg)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		s := a.GetMCPServers()
		if len(s) == 1 && s[0].Connected && s[0].Tools > 0 {
			a.agentMu.Lock()
			reset := a.currentAgent == nil
			a.agentMu.Unlock()
			if _, ok := a.registry.Get("mcp_echo_echo"); ok && reset {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("added server did not connect (or agent not reset): %+v", s)
		}
		time.Sleep(50 * time.Millisecond)
	}
	tool, _ := a.registry.Get("mcp_echo_echo")
	if res := tool.Execute(context.Background(), map[string]string{"text": "x"}); res.Output != "Echo: x" {
		t.Fatalf("echo call: %+v", res)
	}

	a.agentMu.Lock()
	a.currentAgent = &agent.Agent{}
	a.agentMu.Unlock()
	if msg := a.RemoveMCPServer("echo"); msg != "" {
		t.Fatal(msg)
	}
	if _, ok := a.registry.Get("mcp_echo_echo"); ok {
		t.Fatal("removed server's tool still offered")
	}
	if _, ok := a.mcpManager.GetConnection("echo"); ok {
		t.Fatal("removed server still connected")
	}
	if s := a.GetMCPServers(); len(s) != 0 {
		t.Fatalf("removed server still listed: %+v", s)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		a.agentMu.Lock()
		reset := a.currentAgent == nil
		a.agentMu.Unlock()
		if reset {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent not reset after removing the server")
		}
	}
}
