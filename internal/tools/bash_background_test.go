//go:build !windows

package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"openuai/internal/procs"
)

func TestBashRunInBackground(t *testing.T) {
	m := procs.NewManager(procs.Callbacks{})
	defer m.Shutdown()
	start := time.Now()
	r := Bash{Procs: m}.Execute(context.Background(), map[string]string{"command": "sleep 3 && echo BG_DONE", "run_in_background": "true"})
	if r.Error != "" || time.Since(start) > 2*time.Second || !strings.Contains(r.Output, "Started in the background as process p1") {
		t.Fatalf("must return at once with the id: %+v (%s)", r, time.Since(start))
	}
	p, _ := m.Get("p1")
	<-p.Done()
	if rr, _ := m.Read("p1", 0); !strings.Contains(rr.Output, "BG_DONE") {
		t.Fatalf("output: %q", rr.Output)
	}
	if r := (Bash{}).Execute(context.Background(), map[string]string{"command": "true", "run_in_background": "true"}); r.Error == "" {
		t.Fatal("without a process manager it must refuse")
	}
}

func TestKillByUserIsReported(t *testing.T) {
	m := procs.NewManager(procs.Callbacks{})
	defer m.Shutdown()
	p, _ := m.Start("sleep 100", "")
	if err := m.Kill(p.ID(), false); err != nil {
		t.Fatal(err)
	}
	if !p.Info().StoppedByUser || m.ExitObserved(p.ID()) {
		t.Fatalf("user stop must be reported and not observed: %+v", p.Info())
	}
}
