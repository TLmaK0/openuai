package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRestartTargetAcceptsSameRepo(t *testing.T) {
	cwd, _ := os.Getwd()
	got, err := resolveRestartTarget(cwd, ".")
	if err != nil {
		t.Fatalf("own checkout must be accepted: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("expected absolute path, got %q", got)
	}
}

func TestResolveRestartTargetRejects(t *testing.T) {
	cwd, _ := os.Getwd()

	noDevSh := t.TempDir()
	notGit := t.TempDir()
	if err := os.WriteFile(filepath.Join(notGit, "dev.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	for name, dir := range map[string]string{
		"missing":   filepath.Join(noDevSh, "nope"),
		"no dev.sh": noDevSh,
		"not git":   notGit,
	} {
		if _, err := resolveRestartTarget(cwd, dir); err == nil {
			t.Errorf("%s: expected an error for %s", name, dir)
		}
	}
}

func TestSpawnRelaunchLoopNeedsScript(t *testing.T) {
	empty := t.TempDir()
	if _, err := spawnRelaunchLoop(empty, empty); err == nil {
		t.Fatal("expected an error when dev.sh is missing")
	}
}

func TestWriteRelaunchTarget(t *testing.T) {
	f := filepath.Join(t.TempDir(), "target")
	if err := writeRelaunchTarget(f, "/some/dir"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f)
	if string(b) != "/some/dir" {
		t.Fatalf("got %q", b)
	}
}
