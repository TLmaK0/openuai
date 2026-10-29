package agent

import (
	"context"
	"testing"
)

func TestPermissionModes(t *testing.T) {
	asked := 0
	pm := NewPermissionManager(t.TempDir(), func(context.Context, string, string) (PermissionLevel, bool) {
		asked++
		return PermAlways, true
	})
	mk := func(mode string) *Agent { return &Agent{permissions: pm, permMode: mode} }

	// plan: read-only tools only, never asks.
	if ok, _ := mk(PermModePlan).allowTool(context.Background(), "write_file", "session", ""); ok || asked != 0 {
		t.Fatalf("plan must deny writes without asking (ok=%v asked=%d)", ok, asked)
	}
	if ok, _ := mk(PermModePlan).allowTool(context.Background(), "read_file", "none", ""); !ok {
		t.Fatal("plan must allow read-only tools")
	}

	// dontAsk: denies what would prompt, keeps what the user already allowed.
	if ok, why := mk(PermModeDontAsk).allowTool(context.Background(), "bash", "session", ""); ok || asked != 0 || why == "" {
		t.Fatalf("dontAsk must deny without asking (ok=%v asked=%d)", ok, asked)
	}
	pm.AllowForSession("bash")
	if ok, _ := mk(PermModeDontAsk).allowTool(context.Background(), "bash", "session", ""); !ok || asked != 0 {
		t.Fatal("dontAsk must allow tools already allowed by the user")
	}

	// acceptEdits: file edits without asking, other tools ask as usual.
	if ok, _ := mk(PermModeAcceptEdits).allowTool(context.Background(), "write_file", "session", ""); !ok || asked != 0 {
		t.Fatal("acceptEdits must accept file edits without asking")
	}
	if ok, _ := mk(PermModeAcceptEdits).allowTool(context.Background(), "delete_file", "always", ""); !ok || asked != 1 {
		t.Fatalf("acceptEdits must still ask for other tools (asked=%d)", asked)
	}

	// default: asks.
	if ok, _ := mk(PermModeDefault).allowTool(context.Background(), "write_file", "session", ""); !ok || asked != 2 {
		t.Fatalf("default must ask (asked=%d)", asked)
	}
}
