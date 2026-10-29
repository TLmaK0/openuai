package mcpclient

import (
	"os/exec"
	"testing"

	"openuai/internal/config"
)

// Inline sub-agent servers: connected on demand and closed by Stop.
func TestStartScopedEcho(t *testing.T) {
	bin := t.TempDir() + "/mcp-echo"
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/mcp-echo").CombinedOutput(); err != nil {
		t.Fatalf("build mcp-echo: %v\n%s", err, out)
	}
	m, errs := StartScoped([]config.MCPServerConfig{{Name: "echo", Command: bin, AutoStart: true}})
	defer m.Stop()
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(m.AllTools()) == 0 {
		t.Fatal("scoped server has no tools")
	}
	_ = m.Stop()
	if len(m.AllTools()) != 0 {
		t.Fatal("Stop must close scoped servers")
	}
	_, errs = StartScoped([]config.MCPServerConfig{{Name: "broken", Command: "/nonexistent/mcp", AutoStart: true}})
	if len(errs) != 1 {
		t.Fatalf("expected one start error, got %v", errs)
	}
}
