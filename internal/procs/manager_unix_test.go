//go:build !windows

package procs

import (
	"strings"
	"testing"
	"time"
)

func waitFor(cond func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

func TestInteractivePrompt(t *testing.T) {
	var exits []Info
	m := NewManager(Callbacks{OnExit: func(i Info) { exits = append(exits, i) }})
	defer m.Shutdown()

	p, err := m.Start(`printf 'Name: '; read x; echo "hello $x"; exit 3`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool { return p.Info().Status == StatusWaiting }, 6*time.Second) {
		t.Fatalf("process never reported waiting for input; status=%s", p.Info().Status)
	}
	if err := m.Write(p.ID(), []byte("bob\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(6 * time.Second):
		t.Fatal("process did not exit")
	}
	if m.ExitObserved(p.ID()) {
		t.Fatal("exit must not be observed before the agent reads it")
	}
	r, err := m.Read(p.ID(), -1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Output, "hello bob") {
		t.Fatalf("output %q lacks the answer", r.Output)
	}
	if r.Status != StatusExited || r.ExitCode != 3 {
		t.Fatalf("status=%s code=%d", r.Status, r.ExitCode)
	}
	if !m.ExitObserved(p.ID()) {
		t.Fatal("exit should be observed after reading it")
	}
	if len(exits) != 1 {
		t.Fatalf("OnExit called %d times", len(exits))
	}
	// A second read continues where the first stopped.
	if r2, _ := m.Read(p.ID(), -1); r2.Output != "" {
		t.Fatalf("expected no new output, got %q", r2.Output)
	}
}

func TestKillStopsProcessTree(t *testing.T) {
	m := NewManager(Callbacks{})
	defer m.Shutdown()
	p, err := m.Start("sleep 100 & sleep 100", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(p.ID(), true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(8 * time.Second):
		t.Fatal("killed process did not finish")
	}
	if !m.ExitObserved(p.ID()) {
		t.Fatal("a kill by the agent counts as observed")
	}
}

func TestIsATTY(t *testing.T) {
	m := NewManager(Callbacks{})
	defer m.Shutdown()
	p, err := m.Start(`[ -t 0 ] && [ -t 1 ] && echo TTY_OK`, "")
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	r, _ := m.Read(p.ID(), 0)
	if !strings.Contains(r.Output, "TTY_OK") {
		t.Fatalf("process is not attached to a terminal: %q", r.Output)
	}
}

func TestUnknownProcess(t *testing.T) {
	m := NewManager(Callbacks{})
	defer m.Shutdown()
	if _, err := m.Read("nope", -1); err == nil {
		t.Fatal("expected error")
	}
}
