package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"openuai/internal/agent"
	"openuai/internal/tools"
)

func writeTestFile(t *testing.T, path, content string, at time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !at.IsZero() {
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCollectCrashes(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2026, 10, 1, 10, 0, 0, 0, time.Local)
	newer := old.Add(time.Hour)
	writeTestFile(t, filepath.Join(dir, "run-100.txt"), "fatal error: old\n", old)
	writeTestFile(t, filepath.Join(dir, "run-101.txt"), "fatal error: newer\n", newer)
	writeTestFile(t, filepath.Join(dir, "run-102.txt"), "", time.Time{})        // clean run that did not remove it
	writeTestFile(t, filepath.Join(dir, "run-103.txt"), "", time.Time{})        // still running
	writeTestFile(t, filepath.Join(dir, "run-104.txt"), "x", time.Time{})       // still running, even if written
	writeTestFile(t, filepath.Join(dir, "run-7.txt"), "", time.Time{})          // this process
	writeTestFile(t, filepath.Join(dir, "run-abc.txt"), "junk", time.Time{})    // not a run file
	writeTestFile(t, filepath.Join(dir, "notes.txt"), "unrelated", time.Time{}) // untouched

	alive := func(pid int) bool { return pid == 103 || pid == 104 }
	got := collectCrashes(dir, 7, alive)

	want := filepath.Join(dir, "crash-"+newer.Format("20060102-150405")+"-101.txt")
	if got != want {
		t.Fatalf("newest crash = %q, want %q", got, want)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != "fatal error: newer\n" {
		t.Fatalf("trace content = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "crash-"+old.Format("20060102-150405")+"-100.txt")); err != nil {
		t.Fatalf("older crash not kept: %v", err)
	}
	for _, name := range []string{"run-100.txt", "run-101.txt", "run-102.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should be gone, err=%v", name, err)
		}
	}
	for _, name := range []string{"run-103.txt", "run-104.txt", "run-7.txt", "run-abc.txt", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s should be kept: %v", name, err)
		}
	}

	// A second start finds nothing new.
	if got := collectCrashes(dir, 8, alive); got != "" {
		t.Fatalf("second collect = %q, want none", got)
	}
}

func TestPruneCrashes(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 13; i++ {
		writeTestFile(t, filepath.Join(dir, "crash-20261001-1000"+strconv.Itoa(10+i)+"-1.txt"), "x", time.Time{})
	}
	writeTestFile(t, filepath.Join(dir, "run-5.txt"), "", time.Time{})
	pruneCrashes(dir, 10)
	entries, _ := os.ReadDir(dir)
	var traces []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "crash-") {
			traces = append(traces, e.Name())
		}
	}
	if len(traces) != 10 {
		t.Fatalf("kept %d traces, want 10: %v", len(traces), traces)
	}
	if traces[0] != "crash-20261001-100013-1.txt" {
		t.Fatalf("oldest kept = %s, want the three oldest removed", traces[0])
	}
	if _, err := os.Stat(filepath.Join(dir, "run-5.txt")); err != nil {
		t.Fatalf("run file must not be pruned: %v", err)
	}
}

func TestCrashNote(t *testing.T) {
	if crashNote("") != "" {
		t.Fatal("no crash: no note")
	}
	n := crashNote("/c/openuai/crashes/crash-1.txt")
	if !strings.Contains(n, "crashed") || !strings.Contains(n, "`/c/openuai/crashes/crash-1.txt`") {
		t.Fatalf("note = %q", n)
	}
}

// TestCrashOutputRealPanic runs a child process that sets up the crash log
// and dies of an unrecovered panic: the trace must end up on disk, and the
// next start must report it.
func TestCrashOutputRealPanic(t *testing.T) {
	if dir := os.Getenv("OPENUAI_CRASH_CHILD"); dir != "" {
		if _, run := startCrashLog(dir); run == nil {
			os.Exit(3)
		}
		panic("openuai crash test")
	}
	cfg := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashOutputRealPanic$")
	cmd.Env = append(os.Environ(), "OPENUAI_CRASH_CHILD="+cfg)
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("child: err=%v, want exit code 2; output:\n%s", err, out)
	}

	found, run := startCrashLog(cfg)
	if run == nil {
		t.Fatal("crash output not set up")
	}
	defer run.finish()
	if found == "" {
		t.Fatal("the crash of the child was not found")
	}
	b, err := os.ReadFile(found)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "panic: openuai crash test") || !strings.Contains(string(b), "goroutine") {
		t.Fatalf("trace does not hold the panic:\n%s", b)
	}
	if filepath.Dir(found) != crashDir(cfg) {
		t.Fatalf("trace in %s, want %s", filepath.Dir(found), crashDir(cfg))
	}

	// A clean exit leaves nothing behind.
	run.finish()
	if _, err := os.Stat(run.path); !os.IsNotExist(err) {
		t.Fatalf("run file left after a clean exit: %v", err)
	}
}

// The crash note rides on the restart note, so the conversation shows it as a
// notice (with the trace path the frontend displays), not as a user message.
func TestCrashNoteShownAsNotice(t *testing.T) {
	ag := agent.New(agent.Config{Provider: inTreeProvider{name: "x"}, Model: "m", Registry: tools.NewRegistry()})
	trace := "/cfg/openuai/crashes/crash-20261030-120000-42.txt"
	resume, changed := ag.RecoverInterruptedWithNote(nil, crashNote(trace), time.Now())
	if resume || !changed {
		t.Fatalf("resume=%v changed=%v, want a note without resuming", resume, changed)
	}
	h := buildChatHistory(ag.Messages())
	last := h[len(h)-1]
	if last.Role != "notice" || !strings.Contains(last.Content, "crash trace is saved in `"+trace+"`") {
		t.Fatalf("last history entry = %+v", last)
	}
}
