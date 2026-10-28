package main

import (
	"context"
	"testing"
	"time"
)

func TestSubAgentLimiterSharedSlots(t *testing.T) {
	var l subAgentLimiter
	ctx := context.Background()
	r1, err := l.acquire(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	// Nested (no wait): fails at once when the limit is reached.
	if _, err := l.acquire(ctx, 1, false); err == nil {
		t.Fatal("expected limit error")
	}
	// Top level (wait): gets the slot when it is released.
	go func() { time.Sleep(50 * time.Millisecond); r1() }()
	r2, err := l.acquire(ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	r2()
	r2() // releasing twice must not free two slots
	if n := l.inUse(); n != 0 {
		t.Fatalf("in use = %d", n)
	}
	// Waiting stops when the context ends.
	r3, _ := l.acquire(ctx, 1, false)
	defer r3()
	c, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := l.acquire(c, 1, true); err == nil {
		t.Fatal("expected context error")
	}
}

func TestSubAgentScopeDefaultsToMain(t *testing.T) {
	if s := subAgentScopeFrom(context.Background()); s.depth != 0 || s.key != "" || s.registry != nil {
		t.Fatalf("unexpected scope %+v", s)
	}
	ctx := withSubAgentScope(context.Background(), subAgentScope{depth: 2, key: "sa1/x"})
	if s := subAgentScopeFrom(ctx); s.depth != 2 || s.key != "sa1/x" {
		t.Fatalf("scope not carried: %+v", s)
	}
}
