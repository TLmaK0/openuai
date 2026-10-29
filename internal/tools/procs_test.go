//go:build !windows

package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"openuai/internal/procs"
)

func TestProcToolsFlow(t *testing.T) {
	m := procs.NewManager(procs.Callbacks{})
	defer m.Shutdown()
	ctx := context.Background()

	res := ProcStart{m}.Execute(ctx, map[string]string{"command": `printf 'Continue? [y/N] '; read a; echo "answer=$a"; exit 2`})
	if res.Error != "" || !strings.Contains(res.Output, "Started process p1") || !strings.Contains(res.Output, "Continue?") {
		t.Fatalf("start: %+v", res)
	}
	if r := (ProcWrite{m}).Execute(ctx, map[string]string{"id": "p1", "keys": "bogus"}); r.Error == "" {
		t.Fatal("unknown key accepted")
	}
	if r := (ProcWrite{m}).Execute(ctx, map[string]string{"id": "p1", "input": "y", "keys": "enter"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	// A read returns as soon as there is new output (e.g. the echo of "y"),
	// so keep reading until the answer shows up.
	var out strings.Builder
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(out.String(), "answer=y") && time.Now().Before(deadline) {
		r := ProcRead{m}.Execute(ctx, map[string]string{"id": "p1", "wait": "2"})
		if r.Error != "" {
			t.Fatalf("read: %+v", r)
		}
		out.WriteString(r.Output)
	}
	if !strings.Contains(out.String(), "answer=y") {
		t.Fatalf("read: answer missing in %q", out.String())
	}
	if !strings.Contains(ProcList{m}.Execute(ctx, nil).Output, "p1") {
		t.Fatal("list misses p1")
	}
	if r := (ProcRead{m}).Execute(ctx, map[string]string{"id": "nope"}); r.Error == "" {
		t.Fatal("unknown id accepted")
	}
	if r := (ProcStart{m}).Execute(ctx, map[string]string{"command": " "}); r.Error == "" {
		t.Fatal("empty command accepted")
	}
}
