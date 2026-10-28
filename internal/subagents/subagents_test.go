package subagents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openuai/internal/tools"
)

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParse(t *testing.T) {
	d, err := Parse([]byte("---\nname: reviewer\ndescription: Reviews code\ntools: read_file, search_files\nmodel: sonnet\nmaxTurns: 3\nunknownField: x\n---\nYou review code.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "reviewer" || d.Description != "Reviews code" || d.Model != "sonnet" || d.MaxTurns != 3 || d.Prompt != "You review code." {
		t.Fatalf("unexpected definition: %+v", d)
	}
	if !d.HasTools || strings.Join(d.Tools, "|") != "read_file|search_files" {
		t.Fatalf("tools: %v", d.Tools)
	}

	d, err = Parse([]byte("---\nname: lister\ntools:\n  - bash\n  - mcp_github_*\ndisallowedTools: write_file\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(d.Tools, "|") != "bash|mcp_github_*" || strings.Join(d.DisallowedTools, "|") != "write_file" {
		t.Fatalf("lists: %v %v", d.Tools, d.DisallowedTools)
	}

	if d, _ := Parse([]byte("---\nname: all\n---\n")); d.HasTools {
		t.Fatal("no tools field must mean every tool")
	}

	for _, doc := range []string{"# Just notes\n", "---\ndescription: no name\n---\nbody"} {
		if _, err := Parse([]byte(doc)); !errors.Is(err, errNoName) {
			t.Errorf("%q: expected errNoName, got %v", doc, err)
		}
	}
	for _, bad := range []string{"---\nname: [\n---\n", "---\nname: Bad Name\n---\n", "---\nname: x\nmaxTurns: -1\n---\n", "---\nname: x\n"} {
		if _, err := Parse([]byte(bad)); err == nil || errors.Is(err, errNoName) {
			t.Errorf("%q: expected a real error, got %v", bad, err)
		}
	}
}

func TestLoadPrecedence(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	user := t.TempDir()
	write(t, filepath.Join(user, "x.md"), "---\nname: shared\ndescription: user\n---\n")
	write(t, filepath.Join(user, "u.md"), "---\nname: only-user\n---\n")
	write(t, filepath.Join(root, ".openuai", "agents", "shared.md"), "---\nname: shared\ndescription: root\n---\n")
	write(t, filepath.Join(sub, ".openuai", "agents", "nested", "shared.md"), "---\nname: shared\ndescription: closest\n---\n")
	write(t, filepath.Join(root, ".openuai", "agents", "README.md"), "# docs\n")
	write(t, filepath.Join(root, ".openuai", "agents", "broken.md"), "---\nname: [\n---\n")

	s := Load(sub, user)
	if got := s.Defs["shared"]; got == nil || got.Description != "closest" || got.Scope != ScopeProject {
		t.Fatalf("closest project definition must win: %+v", got)
	}
	if got := s.Defs["only-user"]; got == nil || got.Scope != ScopeUser {
		t.Fatalf("user definition missing: %+v", got)
	}
	if len(s.Defs) != 2 {
		t.Fatalf("expected 2 definitions, got %v", s.Names())
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "broken.md") {
		t.Fatalf("warnings: %v", s.Warnings)
	}
}

func TestLoadStopsAtRepoRoot(t *testing.T) {
	outer := t.TempDir()
	write(t, filepath.Join(outer, ".openuai", "agents", "out.md"), "---\nname: out\n---\n")
	repo := filepath.Join(outer, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if s := Load(repo, ""); s.Defs["out"] != nil {
		t.Fatal("definitions above the repository root must not load")
	}
}

type fakeTool struct{ name string }

func (f fakeTool) Definition() tools.Definition { return tools.Definition{Name: f.name} }
func (f fakeTool) Execute(context.Context, map[string]string) tools.Result {
	return tools.Result{}
}

func names(r *tools.Registry) map[string]bool {
	out := map[string]bool{}
	for _, t := range r.All() {
		out[t.Definition().Name] = true
	}
	return out
}

func TestFilterTools(t *testing.T) {
	reg := tools.NewRegistry()
	for _, n := range []string{"bash", "read_file", "write_file", "spawn_agents", "watch_chat", "unwatch_chat", "mcp_github_create_issue", "mcp_github_list", "mcp_slack_post"} {
		reg.Register(fakeTool{n})
	}

	r, err := FilterTools(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := names(r)
	if !got["bash"] || got["spawn_agents"] || got["watch_chat"] || got["unwatch_chat"] {
		t.Fatalf("default filter: %v", got)
	}

	r, err = FilterTools(reg, &Definition{Name: "x", HasTools: true, Tools: []string{"read_file", "mcp_github_*", "bash"}, DisallowedTools: []string{"bash"}})
	if err != nil {
		t.Fatal(err)
	}
	got = names(r)
	if len(got) != 3 || !got["read_file"] || !got["mcp_github_create_issue"] || !got["mcp_github_list"] {
		t.Fatalf("disallowedTools must apply before tools: %v", got)
	}

	r, err = FilterTools(reg, &Definition{Name: "ro", DisallowedTools: []string{"write_file", "mcp_*"}})
	if err != nil {
		t.Fatal(err)
	}
	if got = names(r); got["write_file"] || got["mcp_slack_post"] || !got["read_file"] {
		t.Fatalf("disallowed only: %v", got)
	}

	_, err = FilterTools(reg, &Definition{Name: "none", HasTools: true, Tools: []string{"Read", "spawn_agents"}})
	if err == nil || !strings.Contains(err.Error(), "Read") || !strings.Contains(err.Error(), "spawn_agents") {
		t.Fatalf("expected an error naming the unrecognized tools, got %v", err)
	}
}

func TestResolveModel(t *testing.T) {
	cases := []struct {
		call, def, global, parent string
		available                 []string
		want                      string
		notice                    bool
	}{
		{"", "", "", "p", nil, "p", false},
		{"haiku", "opus", "", "claude-sonnet-4", nil, "haiku", false},
		{"", "sonnet", "", "claude-sonnet-4", nil, "claude-sonnet-4", false},
		{"", "inherit", "opus", "m", nil, "m", false},
		{"", "", "opus", "m", nil, "opus", false},
		{"gpt-x", "", "", "m", []string{"m", "haiku"}, "m", true},
		{"haiku", "", "", "m", []string{"m", "haiku"}, "haiku", false},
		// Like Claude Code: the global default resolves to what it names, even
		// when it is the main agent's family; a global "inherit" means unset.
		{"", "", "sonnet", "claude-sonnet-4", nil, "sonnet", false},
		{"", "", "inherit", "m", nil, "m", false},
		{"sonnet", "", "opus", "claude-sonnet-4", nil, "claude-sonnet-4", false},
	}
	for _, c := range cases {
		got, notice := ResolveModel(c.call, c.def, c.global, c.parent, c.available)
		if got != c.want || (notice != "") != c.notice {
			t.Errorf("%+v: got %q notice=%q", c, got, notice)
		}
	}
}

func TestProjectInstructionsAndPrompt(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "pkg")
	write(t, filepath.Join(root, "AGENTS.md"), "root rules")
	write(t, filepath.Join(sub, "AGENTS.md"), "sub rules")

	ins := ProjectInstructions(sub)
	if i, j := strings.Index(ins, "root rules"), strings.Index(ins, "sub rules"); i < 0 || j < 0 || i > j {
		t.Fatalf("AGENTS.md must load root first: %q", ins)
	}

	p := SystemPrompt(nil, sub)
	if !strings.HasPrefix(p, DefaultPrompt) || !strings.Contains(p, "sub rules") {
		t.Fatalf("default prompt: %q", p)
	}
	p = SystemPrompt(&Definition{Name: "x", Prompt: "CUSTOM", OmitAgentsMd: true}, sub)
	if !strings.HasPrefix(p, "CUSTOM") || strings.Contains(p, "root rules") {
		t.Fatalf("omitAgentsMd prompt: %q", p)
	}
}
