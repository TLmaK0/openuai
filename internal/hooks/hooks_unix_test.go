//go:build !windows

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func set(t *testing.T, cfg Config) *Set {
	t.Helper()
	s := &Set{}
	s.Add(cfg, "test")
	if len(s.Warnings) > 0 {
		t.Fatal(s.Warnings)
	}
	return s
}

func cmd(c string) []Handler { return []Handler{{Type: "command", Command: c}} }

func TestBlockRmRf(t *testing.T) {
	s := set(t, Config{PreToolUse: {{Matcher: "bash", Hooks: cmd(`grep -q 'rm -rf' && { echo 'rm -rf is not allowed' >&2; exit 2; }; exit 0`)}}})
	r := s.Run(context.Background(), PreToolUse, "bash", map[string]any{"tool_input": map[string]any{"command": "rm -rf /tmp/x"}}, t.TempDir())
	if r.Decision != "deny" || !strings.Contains(r.Reason, "rm -rf is not allowed") {
		t.Fatalf("got %+v", r)
	}
	if r := s.Run(context.Background(), PreToolUse, "bash", map[string]any{"tool_input": map[string]any{"command": "ls"}}, t.TempDir()); r.Decision != "" || r.Ran != 1 {
		t.Fatalf("ls must pass: %+v", r)
	}
	if r := s.Run(context.Background(), PreToolUse, "write_file", nil, t.TempDir()); r.Ran != 0 {
		t.Fatal("matcher must filter by tool name")
	}
}

func TestJSONOutputAndPrecedence(t *testing.T) {
	s := set(t, Config{PreToolUse: {
		{Matcher: "write_.*", Hooks: cmd(`echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"path":"/x","n":3},"additionalContext":"CTX1"}}'`)},
	}})
	r := s.Run(context.Background(), PreToolUse, "write_file", nil, t.TempDir())
	if r.Decision != "allow" || r.UpdatedInput["path"] != "/x" || len(r.Context) != 1 || r.Context[0] != "CTX1" {
		t.Fatalf("got %+v", r)
	}
	s.Add(Config{PreToolUse: {{Hooks: cmd(`echo '{"decision":"block","reason":"nope"}'`)}}}, "test")
	if r := s.Run(context.Background(), PreToolUse, "write_file", nil, t.TempDir()); r.Decision != "deny" || r.Reason != "nope" {
		t.Fatalf("deny must win over allow: %+v", r)
	}
}

func TestFailuresAreReported(t *testing.T) {
	s := set(t, Config{PostToolUse: {{Hooks: []Handler{
		{Type: "command", Command: "sleep 5", Timeout: 1},
		{Type: "command", Command: "echo oops >&2; exit 1"},
		{Type: "command", Command: "echo '{bad json}'"},
	}}}})
	r := s.Run(context.Background(), PostToolUse, "bash", nil, t.TempDir())
	if len(r.Errors) != 3 || r.Decision != "" {
		t.Fatalf("got %+v", r)
	}
	all := strings.Join(r.Errors, "|")
	for _, want := range []string{"timed out", "exit 1", "oops", "invalid JSON"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in %s", want, all)
		}
	}
}

func TestStdinAndPlainContext(t *testing.T) {
	s := set(t, Config{UserPromptSubmit: {{Matcher: "ignored", Hooks: cmd(`cat | grep -o '"prompt":"[^"]*"'; echo "dir=$OPENUAI_PROJECT_DIR"`)}}})
	dir := t.TempDir()
	r := s.Run(context.Background(), UserPromptSubmit, "", map[string]any{"prompt": "hola68"}, dir)
	if len(r.Context) != 1 || !strings.Contains(r.Context[0], `"prompt":"hola68"`) || !strings.Contains(r.Context[0], "dir="+dir) {
		t.Fatalf("got %+v", r)
	}
}

func TestLoadFilesAndWarnings(t *testing.T) {
	cfgDir, work := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(work, ".openuai"), 0o755)
	_ = os.WriteFile(filepath.Join(cfgDir, "hooks.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"true"}]}],"Bogus":[]}}`), 0o600)
	_ = os.WriteFile(filepath.Join(work, ".openuai", "hooks.json"), []byte(`{"hooks":{"PreToolUse":[{"matcher":"bash","hooks":[{"type":"command","command":"true"},{"type":"http","url":"x"}]}]}}`), 0o600)
	_ = os.WriteFile(filepath.Join(work, ".openuai", "hooks.local.json"), []byte(`not json`), 0o600)
	s := Load(cfgDir, work)
	if len(s.Entries) != 2 || s.Entries[0].Source != "user" || s.Entries[1].Source != "project" {
		t.Fatalf("entries: %+v", s.Entries)
	}
	if len(s.Warnings) != 3 {
		t.Fatalf("warnings: %v", s.Warnings)
	}
}

func TestMatcherRules(t *testing.T) {
	for m, cases := range map[string]map[string]bool{
		"":                 {"bash": true},
		"*":                {"x": true},
		"bash":             {"bash": true, "bash_sudo": false},
		"write_file|bash":  {"bash": true, "write_file": true, "read_file": false},
		"write_file, bash": {"bash": true},
		"^mcp_github_.*":   {"mcp_github_get": true, "bash": false},
		"file\\b":          {"write_file": true},
	} {
		f, err := compileMatcher(PreToolUse, m)
		if err != nil {
			t.Fatal(err)
		}
		for v, want := range cases {
			if f(v) != want {
				t.Errorf("matcher %q on %q: got %v", m, v, !want)
			}
		}
	}
}
