package subagents

import (
	"path/filepath"
	"testing"

	"openuai/internal/tools"
)

func TestBuiltinsLoaded(t *testing.T) {
	s := Load(t.TempDir(), "")
	for _, n := range []string{BuiltinExplore, BuiltinGeneral} {
		if d := s.Defs[n]; d == nil || d.Scope != ScopeBuiltin || d.Description == "" || d.Prompt == "" {
			t.Fatalf("built-in %q missing or incomplete: %+v", n, d)
		}
	}
}

func TestBuiltinOverriddenByFile(t *testing.T) {
	user := t.TempDir()
	write(t, filepath.Join(user, "explore.md"), "---\nname: explore\ndescription: mine\n---\n")
	s := Load(t.TempDir(), user)
	if d := s.Defs[BuiltinExplore]; d == nil || d.Description != "mine" || d.Scope != ScopeUser {
		t.Fatalf("user definition must replace the built-in: %+v", d)
	}
}

func TestExploreIsReadOnly(t *testing.T) {
	reg := tools.NewRegistry()
	for _, n := range []string{"read_file", "list_dir", "search_files", "git_log", "bash", "write_file", "delete_file", "git_commit", "spawn_agents"} {
		reg.Register(fakeTool{name: n})
	}
	var explore *Definition
	for _, d := range Builtins() {
		if d.Name == BuiltinExplore {
			explore = d
		}
	}
	r, err := FilterTools(reg, explore)
	if err != nil {
		t.Fatal(err)
	}
	got := names(r)
	for _, bad := range []string{"bash", "write_file", "delete_file", "git_commit", "spawn_agents"} {
		if got[bad] {
			t.Errorf("explore must not get %s", bad)
		}
	}
	if !got["read_file"] || !got["search_files"] {
		t.Errorf("explore lacks read tools: %v", got)
	}
}
