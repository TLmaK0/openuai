package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Crash traces (#86). The Go runtime prints a fatal error or an unrecovered
// panic to stderr and exits, so a crash of an app started from the desktop
// left no trace anywhere. Each run hands the runtime a file of its own,
// <config dir>/crashes/run-<pid>.txt, as an extra crash output: it stays
// empty unless the process crashes. The next start turns a non-empty one into
// crash-<time>-<pid>.txt, reports it, and removes the empty ones.

const (
	crashDirName   = "crashes"
	crashRunPrefix = "run-"
	crashPrefix    = "crash-"
	crashSuffix    = ".txt"
	// crashKeep is how many crash traces are kept.
	crashKeep = 10
)

// lastCrashTrace is the trace of a crash of the previous run, found at this
// start ("" when it did not crash). Set once in main, before the app starts.
var lastCrashTrace string

// crashRun is this run's crash output file.
type crashRun struct {
	path string
}

func crashDir(configDir string) string {
	return filepath.Join(configDir, crashDirName)
}

// startCrashLog collects the crashes left by previous runs and makes the Go
// runtime also write this run's fatal errors to a file. It returns the newest
// crash trace found ("" if none) and this run's file (nil when it could not
// be set up; the runtime still prints to stderr).
func startCrashLog(configDir string) (string, *crashRun) {
	dir := crashDir(configDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "openuai: cannot create %s: %v\n", dir, err)
		return "", nil
	}
	found := collectCrashes(dir, os.Getpid(), processAlive)
	pruneCrashes(dir, crashKeep)

	path := filepath.Join(dir, crashRunPrefix+strconv.Itoa(os.Getpid())+crashSuffix)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "openuai: cannot open crash output %s: %v\n", path, err)
		return found, nil
	}
	// SetCrashOutput duplicates the descriptor: f can be closed.
	err = debug.SetCrashOutput(f, debug.CrashOptions{})
	f.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "openuai: cannot set crash output: %v\n", err)
		_ = os.Remove(path)
		return found, nil
	}
	return found, &crashRun{path: path}
}

// finish is called on a clean exit: this run did not crash.
func (r *crashRun) finish() {
	if r == nil {
		return
	}
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	_ = os.Remove(r.path)
}

// collectCrashes goes through the run files of other processes that are no
// longer running: an empty one (a run that did not crash but did not get to
// remove it, e.g. one that exited for a restart) is removed, a non-empty one
// is renamed to a crash trace. It returns the newest crash trace found.
func collectCrashes(dir string, self int, alive func(int) bool) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestAt time.Time
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, crashRunPrefix) || !strings.HasSuffix(name, crashSuffix) {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, crashRunPrefix), crashSuffix))
		if err != nil || pid == self || alive(pid) {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() == 0 {
			_ = os.Remove(path)
			continue
		}
		at := info.ModTime()
		dest := filepath.Join(dir, crashPrefix+at.Format("20060102-150405")+"-"+strconv.Itoa(pid)+crashSuffix)
		if err := os.Rename(path, dest); err != nil {
			continue
		}
		if newest == "" || at.After(newestAt) {
			newest, newestAt = dest, at
		}
	}
	return newest
}

// pruneCrashes keeps the newest keep crash traces.
func pruneCrashes(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var traces []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), crashPrefix) && strings.HasSuffix(e.Name(), crashSuffix) {
			traces = append(traces, e.Name())
		}
	}
	// The names start with the time, so they sort chronologically.
	sort.Strings(traces)
	for len(traces) > keep {
		_ = os.Remove(filepath.Join(dir, traces[0]))
		traces = traces[1:]
	}
}

// crashNote is the text added to the restart note in the conversation when
// the previous run crashed. The frontend shows the trace path from it.
func crashNote(trace string) string {
	if trace == "" {
		return ""
	}
	return "OpenUAI crashed before this start; the crash trace is saved in `" + trace + "`."
}
