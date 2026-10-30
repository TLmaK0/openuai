//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runDevLoop runs the real dev.sh relaunch loop on a fake worktree whose
// `dev.sh --once` exits with the given codes in turn (the last one repeats).
// It returns the loop's exit code, how many launches happened and the loop's
// output.
func runDevLoop(t *testing.T, env []string, codes ...int) (int, int, string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	script, err := filepath.Abs("dev.sh")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	var list []string
	for _, c := range codes {
		list = append(list, strconv.Itoa(c))
	}
	stub := `#!/usr/bin/env bash
n=$(cat launches 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > launches
codes=(` + strings.Join(list, " ") + `)
i=$((n - 1))
if [ "$i" -ge "${#codes[@]}" ]; then i=$((${#codes[@]} - 1)); fi
exit "${codes[$i]}"
`
	if err := os.WriteFile(filepath.Join(work, "dev.sh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "--worktree", work)
	cmd.Dir = work
	cmd.Env = append(append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir()), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running the loop: %v", err)
		}
		code = ee.ExitCode()
	}
	b, _ := os.ReadFile(filepath.Join(work, "launches"))
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return code, n, string(out)
}

func TestDevLoopNormalQuitDoesNotRelaunch(t *testing.T) {
	code, n, out := runDevLoop(t, nil, 0)
	if code != 0 || n != 1 {
		t.Fatalf("code=%d launches=%d, want 0 and 1\n%s", code, n, out)
	}
}

func TestDevLoopBuildFailureAndStopSignalsDoNotRelaunch(t *testing.T) {
	for _, c := range []int{1, 129, 130, 143} {
		code, n, out := runDevLoop(t, nil, c)
		if code != c || n != 1 {
			t.Fatalf("exit %d: code=%d launches=%d, want %d and 1\n%s", c, code, n, c, out)
		}
	}
}

func TestDevLoopRelaunchesAfterCrash(t *testing.T) {
	// Go fatal error, then SIGSEGV, then a normal quit.
	code, n, out := runDevLoop(t, nil, 2, 139, 0)
	if code != 0 || n != 3 {
		t.Fatalf("code=%d launches=%d, want 0 and 3\n%s", code, n, out)
	}
	if !strings.Contains(out, "the app crashed (code 2); relaunching") || !strings.Contains(out, "the app crashed (code 139); relaunching") {
		t.Fatalf("missing crash relaunch messages:\n%s", out)
	}
}

func TestDevLoopStopsAfterCrashCap(t *testing.T) {
	code, n, out := runDevLoop(t, nil, 2, 134, 2, 2, 0)
	// Three crash relaunches allowed: the fourth crash stops the loop.
	if code != 2 || n != 4 {
		t.Fatalf("code=%d launches=%d, want 2 and 4\n%s", code, n, out)
	}
	if !strings.Contains(out, "not relaunching it again") || !strings.Contains(out, "openuai/crashes") {
		t.Fatalf("missing cap message:\n%s", out)
	}
}

func TestDevLoopCrashWindowForgetsOldCrashes(t *testing.T) {
	// With a 1 s window, the 1 s pause between launches lets every crash
	// fall out of it, so one crash relaunch at a time never hits the cap.
	code, n, out := runDevLoop(t, []string{"OPENUAI_CRASH_RELAUNCH_MAX=1", "OPENUAI_CRASH_RELAUNCH_WINDOW=1"}, 2, 2, 2, 0)
	if code != 0 || n != 4 {
		t.Fatalf("code=%d launches=%d, want 0 and 4\n%s", code, n, out)
	}
}

// A restart into another worktree whose app crashes right away still goes
// back to the previous directory (the crash relaunch does not take over).
func TestDevLoopWorktreeSwitchFallbackKept(t *testing.T) {
	script, err := filepath.Abs("dev.sh")
	if err != nil {
		t.Fatal(err)
	}
	a, b := t.TempDir(), t.TempDir()
	stubA := `#!/usr/bin/env bash
n=$(cat launches 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > launches
if [ "$n" -eq 1 ]; then echo "` + b + `" > "$OPENUAI_RELAUNCH_FILE"; exit 42; fi
exit 0
`
	stubB := `#!/usr/bin/env bash
n=$(cat launches 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > launches
exit 2
`
	if err := os.WriteFile(filepath.Join(a, "dev.sh"), []byte(stubA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "dev.sh"), []byte(stubB), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "--worktree", a)
	cmd.Dir = a
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("loop: %v\n%s", err, out)
	}
	launches := func(dir string) string {
		b, _ := os.ReadFile(filepath.Join(dir, "launches"))
		return strings.TrimSpace(string(b))
	}
	if launches(a) != "2" || launches(b) != "1" {
		t.Fatalf("launches a=%s b=%s, want 2 and 1\n%s", launches(a), launches(b), out)
	}
	if !strings.Contains(string(out), "going back to "+a) {
		t.Fatalf("no fallback message:\n%s", out)
	}
}
