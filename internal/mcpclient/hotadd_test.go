package mcpclient

import (
	"context"
	"os/exec"
	"testing"

	"openuai/internal/config"
	"openuai/internal/tools"
)

// A manager created with no servers connects one added at runtime, its tools
// reach the registry, and removing it disconnects it and drops its tools.
func TestManagerAddRemoveServerAtRuntime(t *testing.T) {
	bin := t.TempDir() + "/mcp-echo"
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/mcp-echo").CombinedOutput(); err != nil {
		t.Fatalf("build mcp-echo: %v\n%s", err, out)
	}
	m := NewManager(nil)
	defer m.Stop()
	reg := tools.NewRegistry()

	if err := m.AddServer(config.MCPServerConfig{Name: "echo", Command: bin, AutoStart: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.GetConnection("echo"); !ok {
		t.Fatal("added server is not connected")
	}
	RegisterMCPTools(reg, m, nil)
	tool, ok := reg.Get("mcp_echo_echo")
	if !ok {
		t.Fatal("echo tool not registered")
	}
	res := tool.Execute(context.Background(), map[string]string{"text": "hi"})
	if res.Error != "" || res.Output != "Echo: hi" {
		t.Fatalf("echo call: %+v", res)
	}

	m.RemoveServer("echo")
	if _, ok := m.GetConnection("echo"); ok {
		t.Fatal("removed server still connected")
	}
	if len(m.AllTools()) != 0 {
		t.Fatal("removed server still lists tools")
	}
	RegisterMCPTools(reg, m, nil)
	if _, ok := reg.Get("mcp_echo_echo"); ok {
		t.Fatal("tool of a removed server is still registered")
	}
	if err := m.AuthenticateServer("echo"); err == nil {
		t.Fatal("removed server config must be forgotten")
	}
}

// Syncing drops only this manager's tools, never local or other managers' tools.
func TestRegisterMCPToolsKeepsOtherTools(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFile{})
	local := tools.ReadFile{}.Definition().Name
	other := NewManager(nil)
	reg.Register(&MCPTool{localName: "mcp_x_y", serverName: "x", remoteName: "y", manager: other})
	RegisterMCPTools(reg, NewManager(nil), nil)
	if _, ok := reg.Get(local); !ok {
		t.Fatal("local tool dropped")
	}
	if _, ok := reg.Get("mcp_x_y"); !ok {
		t.Fatal("another manager's tool dropped")
	}
	RegisterMCPTools(reg, other, nil)
	if _, ok := reg.Get("mcp_x_y"); ok {
		t.Fatal("stale tool of the synced manager kept")
	}
}
