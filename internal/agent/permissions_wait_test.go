package agent

import (
	"context"
	"testing"
	"time"
)

// A pending approval dialog must not block other agents, and Stop (ctx
// cancel) must end every wait at once.
func TestCheckWaitDoesNotBlockAndIsCancellable(t *testing.T) {
	started := make(chan struct{})
	pm := NewPermissionManager(t.TempDir(), func(ctx context.Context, tool, cmd string) (PermissionLevel, bool) {
		close(started)
		<-ctx.Done()
		return PermAlways, false
	})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan bool, 1)
	go func() { first <- pm.Check(ctx, "bash", "always", "x") }()
	<-started

	free := make(chan bool, 1)
	go func() { free <- pm.Check(context.Background(), "read_file", "none", "") }()
	select {
	case v := <-free:
		if !v {
			t.Fatal("tool without permission denied")
		}
	case <-time.After(time.Second):
		t.Fatal("a pending dialog blocked a tool that needs no permission")
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	second := make(chan bool, 1)
	go func() { second <- pm.Check(ctx2, "write_file", "session", "") }()
	time.Sleep(50 * time.Millisecond)
	cancel2()
	select {
	case v := <-second:
		if v {
			t.Fatal("cancelled wait must deny")
		}
	case <-time.After(time.Second):
		t.Fatal("waiting for the dialog could not be cancelled")
	}

	cancel()
	select {
	case v := <-first:
		if v {
			t.Fatal("cancelled request must deny")
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not end the open approval")
	}
}
