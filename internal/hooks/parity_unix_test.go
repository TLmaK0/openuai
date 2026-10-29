//go:build !windows

package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"openuai/internal/config"
	"openuai/internal/mcpclient"
)

func withEnv(t *testing.T, e Env) {
	t.Helper()
	SetEnv(e)
	t.Cleanup(func() { SetEnv(Env{}) })
}

func bashInput(c string) map[string]any {
	return map[string]any{"tool_name": "bash", "tool_input": map[string]any{"command": c}}
}

// Exec form: no shell, placeholders substituted in command and args.
func TestExecFormAndPlaceholders(t *testing.T) {
	proj := t.TempDir()
	withEnv(t, Env{ProjectDir: proj})
	script := filepath.Join(proj, "hook.sh")
	_ = os.WriteFile(script, []byte("#!/bin/sh\necho \"$1|$2|$OPENUAI_PROJECT_DIR\"\n"), 0o755)
	s := set(t, Config{UserPromptSubmit: {{Hooks: []Handler{{Type: "command", Command: "${OPENUAI_PROJECT_DIR}/hook.sh", Args: []string{"it's $HOME `x`", "${OPENUAI_PROJECT_DIR}"}}}}}})
	r := s.Run(context.Background(), UserPromptSubmit, "", nil, t.TempDir())
	if len(r.Context) != 1 || r.Context[0] != "it's $HOME `x`|"+proj+"|"+proj {
		t.Fatalf("got %+v", r)
	}
}

func TestOnFailureBlock(t *testing.T) {
	missing := []Handler{{Type: "command", Command: "/nonexistent/check", Args: []string{}}}
	block := []Handler{{Type: "command", Command: "/nonexistent/check", Args: []string{}, OnFailure: "block"}}
	// Default (continue): a hook that cannot start is a non-blocking error.
	r := set(t, Config{PreToolUse: {{Hooks: missing}}}).Run(context.Background(), PreToolUse, "bash", bashInput("ls"), t.TempDir())
	if r.Decision != "" || len(r.Errors) != 1 {
		t.Fatalf("continue: %+v", r)
	}
	r = set(t, Config{PreToolUse: {{Hooks: block}}}).Run(context.Background(), PreToolUse, "bash", bashInput("ls"), t.TempDir())
	if r.Decision != "deny" || !strings.Contains(r.Reason, `blocking because onFailure is "block"`) {
		t.Fatalf("block: %+v", r)
	}
	// Exit 1 with valid JSON allowing the call is still a failure under block.
	h := []Handler{{Type: "command", Command: `echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'; exit 1`, OnFailure: "block"}}
	if r := set(t, Config{PreToolUse: {{Hooks: h}}}).Run(context.Background(), PreToolUse, "bash", nil, t.TempDir()); r.Decision != "deny" {
		t.Fatalf("exit 1 + block: %+v", r)
	}
	// Timeout under block on UserPromptSubmit blocks the prompt.
	h = []Handler{{Type: "command", Command: "sleep 3", Timeout: 1, OnFailure: "block"}}
	if r := set(t, Config{UserPromptSubmit: {{Hooks: h}}}).Run(context.Background(), UserPromptSubmit, "", nil, t.TempDir()); r.Decision != "block" || !strings.Contains(r.Reason, "timed out") {
		t.Fatalf("timeout + block: %+v", r)
	}
	// No effect on Stop; PermissionRequest denies.
	if r := set(t, Config{Stop: {{Hooks: block}}}).Run(context.Background(), Stop, "", nil, t.TempDir()); r.Decision != "" {
		t.Fatalf("stop: %+v", r)
	}
	if r := set(t, Config{PermissionRequest: {{Hooks: block}}}).Run(context.Background(), PermissionRequest, "bash", nil, t.TempDir()); r.Decision != "deny" {
		t.Fatalf("permission request: %+v", r)
	}
}

func TestShellPowershellRejectedValue(t *testing.T) {
	s := &Set{}
	s.Add(Config{PreToolUse: {{Hooks: []Handler{{Type: "command", Command: "x", Shell: "zsh"}}}}}, "t")
	if len(s.Warnings) != 1 || len(s.Entries) != 0 {
		t.Fatalf("%+v", s)
	}
}

func TestHTTPHook(t *testing.T) {
	var mu sync.Mutex
	var got map[string]any
	var auth, other string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &got)
		auth, other = r.Header.Get("Authorization"), r.Header.Get("X-Other")
		mu.Unlock()
		switch r.URL.Path {
		case "/deny":
			_, _ = w.Write([]byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"http says no"}}`))
		case "/empty":
		case "/text":
			_, _ = w.Write([]byte("hello"))
		case "/500":
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	t.Setenv("HOOK_TOKEN", "s3cret")
	t.Setenv("HOOK_OTHER", "leak")
	h := func(path string, onFailure string) []Handler {
		return []Handler{{Type: "http", URL: srv.URL + path, OnFailure: onFailure,
			Headers:        map[string]string{"Authorization": "Bearer $HOOK_TOKEN", "X-Other": "${HOOK_OTHER}x"},
			AllowedEnvVars: []string{"HOOK_TOKEN"}}}
	}
	r := set(t, Config{PreToolUse: {{Hooks: h("/deny", "")}}}).Run(context.Background(), PreToolUse, "bash", bashInput("ls"), t.TempDir())
	if r.Decision != "deny" || r.Reason != "http says no" {
		t.Fatalf("deny: %+v", r)
	}
	mu.Lock()
	if auth != "Bearer s3cret" || other != "x" || got["hook_event_name"] != "PreToolUse" || got["tool_name"] != "bash" {
		t.Fatalf("request: auth=%q other=%q body=%v", auth, other, got)
	}
	mu.Unlock()
	if r := set(t, Config{PreToolUse: {{Hooks: h("/empty", "")}}}).Run(context.Background(), PreToolUse, "bash", nil, t.TempDir()); r.Decision != "" || len(r.Errors) != 0 {
		t.Fatalf("empty: %+v", r)
	}
	if r := set(t, Config{UserPromptSubmit: {{Hooks: h("/text", "")}}}).Run(context.Background(), UserPromptSubmit, "", nil, t.TempDir()); len(r.Errors) != 1 || len(r.Context) != 0 {
		t.Fatalf("plain text must be an error, not context: %+v", r)
	}
	if r := set(t, Config{PreToolUse: {{Hooks: h("/500", "")}}}).Run(context.Background(), PreToolUse, "bash", nil, t.TempDir()); r.Decision != "" || !strings.Contains(strings.Join(r.Errors, ""), "HTTP 500") {
		t.Fatalf("500: %+v", r)
	}
	if r := set(t, Config{PreToolUse: {{Hooks: h("/500", "block")}}}).Run(context.Background(), PreToolUse, "bash", nil, t.TempDir()); r.Decision != "deny" {
		t.Fatalf("500 + block: %+v", r)
	}
}

// mcp_tool against the real mcp-echo server through the MCP client.
func TestMCPToolHookWithEcho(t *testing.T) {
	bin := t.TempDir() + "/mcp-echo"
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/mcp-echo").CombinedOutput(); err != nil {
		t.Fatalf("build mcp-echo: %v\n%s", err, out)
	}
	m, errs := mcpclient.StartScoped([]config.MCPServerConfig{{Name: "echo", Command: bin, AutoStart: true}})
	defer m.Stop()
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	withEnv(t, Env{MCP: func(ctx context.Context, server, tool string, args map[string]any) (string, bool, error) {
		res, err := m.CallTool(ctx, server, tool, args)
		if err != nil {
			return "", false, err
		}
		return mcpclient.ContentToString(res.Content), res.IsError, nil
	}})
	s := set(t, Config{UserPromptSubmit: {{Hooks: []Handler{{Type: "mcp_tool", Server: "echo", Tool: "echo", Input: map[string]any{"text": "prompt=${prompt}"}}}}}})
	r := s.Run(context.Background(), UserPromptSubmit, "", map[string]any{"prompt": "hi"}, t.TempDir())
	if len(r.Context) != 1 || r.Context[0] != "Echo: prompt=hi" {
		t.Fatalf("got %+v", r)
	}
	// Unknown tool: a non-blocking error.
	s = set(t, Config{PreToolUse: {{Hooks: []Handler{{Type: "mcp_tool", Server: "echo", Tool: "nope"}}}}})
	if r := s.Run(context.Background(), PreToolUse, "bash", nil, t.TempDir()); r.Decision != "" || len(r.Errors) != 1 {
		t.Fatalf("unknown tool: %+v", r)
	}
	// SessionStart at launch skips mcp_tool hooks.
	s = set(t, Config{SessionStart: {{Hooks: []Handler{{Type: "mcp_tool", Server: "echo", Tool: "echo", Input: map[string]any{"text": "x"}}}}}})
	if r := s.Run(WithoutMCP(context.Background()), SessionStart, "startup", nil, t.TempDir()); r.Ran != 0 {
		t.Fatalf("launch: %+v", r)
	}
}

func TestMCPToolIsErrorAndSubstitution(t *testing.T) {
	var gotArgs map[string]any
	withEnv(t, Env{MCP: func(_ context.Context, server, tool string, args map[string]any) (string, bool, error) {
		gotArgs = args
		if tool == "fail" {
			return "scan failed", true, nil
		}
		return `{"decision":"block","reason":"scan found secrets"}`, false, nil
	}})
	in := map[string]any{"tool_name": "write_file", "tool_input": map[string]any{"path": "/a/b.go", "n": 3.0}}
	s := set(t, Config{PostToolUse: {{Hooks: []Handler{{Type: "mcp_tool", Server: "s", Tool: "scan", Input: map[string]any{"file": "${tool_input.path}", "n": "${tool_input.n}", "nested": []any{"x ${tool_name}"}}}}}}})
	r := s.Run(context.Background(), PostToolUse, "write_file", in, t.TempDir())
	if r.Decision != "block" || r.Reason != "scan found secrets" {
		t.Fatalf("got %+v", r)
	}
	if gotArgs["file"] != "/a/b.go" || gotArgs["n"] != 3.0 || gotArgs["nested"].([]any)[0] != "x write_file" {
		t.Fatalf("args %v", gotArgs)
	}
	s = set(t, Config{PreToolUse: {{Hooks: []Handler{{Type: "mcp_tool", Server: "s", Tool: "fail"}}}}})
	if r := s.Run(context.Background(), PreToolUse, "bash", nil, t.TempDir()); r.Decision != "" || !strings.Contains(strings.Join(r.Errors, ""), "scan failed") {
		t.Fatalf("isError: %+v", r)
	}
}

func TestPromptHook(t *testing.T) {
	var mu sync.Mutex
	var user, model string
	answer := `Sure: {"ok": false, "reason": "tests are not run"}`
	withEnv(t, Env{Prompt: func(_ context.Context, m, system, u string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		user, model = u, m
		if !strings.Contains(system, `"ok"`) {
			t.Error("system prompt must ask for the JSON decision")
		}
		return answer, nil
	}})
	h := []Handler{{Type: "prompt", Prompt: `Check \$1.00 and $ARGUMENTS`, Model: "small"}}
	r := set(t, Config{Stop: {{Hooks: h}}}).Run(context.Background(), Stop, "", map[string]any{"last_assistant_message": "done"}, t.TempDir())
	if r.Decision != "block" || r.Reason != "tests are not run" {
		t.Fatalf("stop: %+v", r)
	}
	if !strings.HasPrefix(user, "Check $1.00 and {") || !strings.Contains(user, `"last_assistant_message":"done"`) || model != "small" {
		t.Fatalf("prompt %q model %q", user, model)
	}
	// PreToolUse: deny and end the turn, unless continueOnBlock.
	r = set(t, Config{PreToolUse: {{Hooks: h}}}).Run(context.Background(), PreToolUse, "bash", nil, t.TempDir())
	if r.Decision != "deny" || !r.StopAll {
		t.Fatalf("pre: %+v", r)
	}
	h[0].ContinueOnBlock = true
	if r := set(t, Config{PreToolUse: {{Hooks: h}}}).Run(context.Background(), PreToolUse, "bash", nil, t.TempDir()); r.Decision != "deny" || r.StopAll {
		t.Fatalf("pre continueOnBlock: %+v", r)
	}
	// impossible lets a Stop end.
	mu.Lock()
	answer = `{"ok": false, "reason": "never", "impossible": true}`
	mu.Unlock()
	if r := set(t, Config{Stop: {{Hooks: h}}}).Run(context.Background(), Stop, "", nil, t.TempDir()); r.Decision != "" {
		t.Fatalf("impossible: %+v", r)
	}
	// Bad answers are errors; prompt hooks cannot run on SessionStart.
	mu.Lock()
	answer = "no json"
	mu.Unlock()
	if r := set(t, Config{UserPromptSubmit: {{Hooks: h}}}).Run(context.Background(), UserPromptSubmit, "", nil, t.TempDir()); r.Decision != "" || len(r.Errors) != 1 {
		t.Fatalf("bad answer: %+v", r)
	}
	s := &Set{}
	s.Add(Config{SessionStart: {{Hooks: h}}, PermissionRequest: {{Hooks: []Handler{{Type: "agent", Prompt: "x"}}}}}, "t")
	if len(s.Entries) != 0 || len(s.Warnings) != 2 {
		t.Fatalf("unsupported types: %+v", s)
	}
}

func TestAgentHook(t *testing.T) {
	var dir string
	withEnv(t, Env{Agent: func(_ context.Context, _, _, user, d string) (string, error) {
		dir = d
		return `{"ok": false, "reason": "file missing"}`, nil
	}})
	wd := t.TempDir()
	h := []Handler{{Type: "agent", Prompt: "verify $ARGUMENTS"}}
	// Agent hooks behave like continueOnBlock: true.
	r := set(t, Config{PostToolUse: {{Hooks: h}}}).Run(context.Background(), PostToolUse, "bash", nil, wd)
	if r.Decision != "block" || r.StopAll || r.Reason != "file missing" || dir != wd {
		t.Fatalf("got %+v dir=%s", r, dir)
	}
}

func TestAsyncHooks(t *testing.T) {
	type del struct {
		text string
		wake bool
	}
	ch := make(chan del, 4)
	withEnv(t, Env{Deliver: func(text string, wake bool) { ch <- del{text, wake} }})
	s := set(t, Config{PostToolUse: {{Hooks: []Handler{
		{Type: "command", Command: `sleep 0.2; echo noise; echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"tests passed"}}'`, Async: true},
		{Type: "command", Command: `sleep 0.2; echo 'build broke' >&2; exit 2`, AsyncRewake: true},
	}}}})
	start := time.Now()
	r := s.Run(context.Background(), PostToolUse, "bash", nil, t.TempDir())
	if time.Since(start) > 150*time.Millisecond || r.Decision != "" || len(r.Context) != 0 {
		t.Fatalf("async hooks must not block: %+v", r)
	}
	var got []del
	for len(got) < 2 {
		select {
		case d := <-ch:
			got = append(got, d)
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout, got %+v", got)
		}
	}
	var ctx, wake bool
	for _, d := range got {
		if strings.Contains(d.text, "tests passed") && !d.wake {
			ctx = true
		}
		if strings.Contains(d.text, "build broke") && d.wake {
			wake = true
		}
	}
	if !ctx || !wake {
		t.Fatalf("deliveries %+v", got)
	}
}

func TestIfRules(t *testing.T) {
	cases := []struct {
		rule string
		in   map[string]any
		want bool
	}{
		{"Bash(git *)", bashInput("FOO=bar git push"), true},
		{"Bash(git *)", bashInput("npm test && git push"), true},
		{"bash(git *)", bashInput("ls -la | grep x"), false},
		{"Bash(rm *)", bashInput("echo $(rm -rf /)"), true},
		{"Bash(rm *)", bashInput("echo `rm -rf /`"), true},
		{"Bash(rm *)", bashInput("echo $(date)"), false},
		{"Bash(git push *)", bashInput("echo $(date)"), true},
		{"Bash(git *)", bashInput("echo 'unbalanced"), true},
		{"Bash(npm run:*)", bashInput("npm run build 2>&1"), true},
		{"Bash", bashInput("anything"), true},
		{"Write(*.go)", map[string]any{"tool_name": "write_file", "tool_input": map[string]any{"path": "pkg/a.go"}}, true},
		{"write_file(*.go)", map[string]any{"tool_name": "write_file", "tool_input": map[string]any{"path": "a.ts"}}, false},
		{"Edit(src/**)", map[string]any{"tool_name": "write_file", "tool_input": map[string]any{"path": "src/x/y.ts"}}, true},
		{"Edit(src/**)", map[string]any{"tool_name": "write_file", "tool_input": map[string]any{"path": "lib/src/y.ts"}}, false},
		{"Edit(**/src/**)", map[string]any{"tool_name": "write_file", "tool_input": map[string]any{"path": "lib/src/y.ts"}}, true},
		{"Read", map[string]any{"tool_name": "write_file"}, false},
		{"WebFetch(domain:example.com)", map[string]any{"tool_name": "web_fetch", "tool_input": map[string]any{"url": "https://docs.example.com/x"}}, true},
	}
	for _, c := range cases {
		if got := ifMatches(c.rule, c.in, "/work"); got != c.want {
			t.Errorf("if %q on %v: got %v", c.rule, c.in["tool_input"], got)
		}
	}
	// `if` filters handlers; on non-tool events a hook with `if` never runs.
	s := set(t, Config{
		PreToolUse:       {{Hooks: []Handler{{Type: "command", Command: "exit 2", If: "Bash(rm *)"}}}},
		UserPromptSubmit: {{Hooks: []Handler{{Type: "command", Command: "echo x", If: "Bash"}}}},
	})
	if r := s.Run(context.Background(), PreToolUse, "bash", bashInput("ls"), t.TempDir()); r.Ran != 0 {
		t.Fatalf("ls: %+v", r)
	}
	if r := s.Run(context.Background(), PreToolUse, "bash", bashInput("rm -rf x"), t.TempDir()); r.Decision != "deny" {
		t.Fatalf("rm: %+v", r)
	}
	if r := s.Run(context.Background(), UserPromptSubmit, "", nil, t.TempDir()); r.Ran != 0 {
		t.Fatalf("non-tool event: %+v", r)
	}
}

func TestStatusMessage(t *testing.T) {
	var got []string
	var mu sync.Mutex
	ctx := WithStatus(context.Background(), func(m string) { mu.Lock(); got = append(got, m); mu.Unlock() })
	s := set(t, Config{PreToolUse: {{Hooks: []Handler{{Type: "command", Command: "true", StatusMessage: "Checking policy…"}}}}})
	s.Run(ctx, PreToolUse, "bash", nil, t.TempDir())
	if len(got) != 1 || got[0] != "Checking policy…" {
		t.Fatalf("got %v", got)
	}
}

func TestOnceSessionHooks(t *testing.T) {
	var r SessionHooks
	dir := t.TempDir()
	flag := filepath.Join(dir, "ok")
	w := r.Register(Config{PreToolUse: {{Hooks: []Handler{
		{Type: "command", Command: "test -f " + flag + " || exit 1", Once: true},
		{Type: "command", Command: "true"},
	}}}}, "skill:deploy")
	if len(w) != 0 || len(r.Set().Entries) != 2 {
		t.Fatalf("register: %v %+v", w, r.Set())
	}
	r.Set().Run(context.Background(), PreToolUse, "bash", nil, dir)
	if len(r.Set().Entries) != 2 {
		t.Fatal("a failed once-hook must stay")
	}
	_ = os.WriteFile(flag, nil, 0o600)
	r.Set().Run(context.Background(), PreToolUse, "bash", nil, dir)
	if e := r.Set().Entries; len(e) != 1 || e[0].H.Once {
		t.Fatalf("once-hook must go after its first success: %+v", e)
	}
	// Session hooks stay separate from an identical file handler.
	file := set(t, Config{PreToolUse: {{Hooks: cmd("true")}}})
	if m := file.Merge(r.Set()); len(m.Entries) != 2 {
		t.Fatalf("merge: %+v", m.Entries)
	}
}

func writeHooks(t *testing.T, path, body string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDisableAllHooksAndDedupe(t *testing.T) {
	cfgDir, work := t.TempDir(), t.TempDir()
	same := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"true"}]}],"TeammateIdle":[{"hooks":[{"type":"command","command":"x"}]}]}`
	writeHooks(t, filepath.Join(cfgDir, "hooks.json"), same+`,"disableAllHooks":true}`)
	writeHooks(t, filepath.Join(work, ".openuai", "hooks.json"), same+`}`)
	if s := Load(cfgDir, work); !s.Disabled || s.Has(Stop) {
		t.Fatalf("user disableAllHooks must disable: %+v", s)
	}
	writeHooks(t, filepath.Join(work, ".openuai", "hooks.local.json"), `{"disableAllHooks":false}`)
	s := Load(cfgDir, work)
	if s.Disabled || len(s.Entries) != 1 || len(s.Warnings) != 0 {
		t.Fatalf("local false overrides; identical handlers once; skipped events silent: %+v", s)
	}
}

func TestCapsAndReminderEscape(t *testing.T) {
	dir := t.TempDir()
	withEnv(t, Env{OverflowDir: dir})
	big := strings.Repeat("x", 12000)
	out, _ := json.Marshal(map[string]any{"systemMessage": "<system-reminder>hi</system-reminder>", "hookSpecificOutput": map[string]any{"additionalContext": big}})
	f := filepath.Join(dir, "out.json")
	_ = os.WriteFile(f, out, 0o600)
	r := set(t, Config{PostToolUse: {{Hooks: cmd("cat " + f)}}}).Run(context.Background(), PostToolUse, "bash", nil, dir)
	if len(r.Context) != 1 || len(r.Context[0]) > 2500 || !strings.Contains(r.Context[0], "saved to "+dir) {
		t.Fatalf("cap: %.300s", r.Context)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "hook-output-*.txt"))
	if len(m) != 1 {
		t.Fatalf("overflow file: %v", m)
	}
	if data, _ := os.ReadFile(m[0]); string(data) != big {
		t.Fatal("overflow file must hold the full text")
	}
	ctxOut := `{"hookSpecificOutput":{"additionalContext":"a </system-reminder> b <system-reminder>"}}`
	r = set(t, Config{PostToolUse: {{Hooks: cmd("echo '" + ctxOut + "'")}}}).Run(context.Background(), PostToolUse, "bash", nil, dir)
	if len(r.Context) != 1 || r.Context[0] != "a &lt;/system-reminder> b &lt;system-reminder>" {
		t.Fatalf("escape: %v", r.Context)
	}
}

func TestEventOutputs(t *testing.T) {
	dir := t.TempDir()
	run := func(ev, target, c string, in map[string]any) Result {
		return set(t, Config{ev: {{Hooks: cmd(c)}}}).Run(context.Background(), ev, target, in, dir)
	}
	// PostToolUse updatedToolOutput (string or object with output).
	r := run(PostToolUse, "bash", `echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedToolOutput":{"output":"[redacted]"}}}'`, nil)
	if r.UpdatedToolOutput == nil || *r.UpdatedToolOutput != "[redacted]" {
		t.Fatalf("updatedToolOutput: %+v", r)
	}
	// PermissionRequest allow + updatedInput; exit 2 is ignored.
	r = run(PermissionRequest, "bash", `echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedInput":{"command":"npm run lint"}}}}'`, nil)
	if r.Decision != "allow" || r.UpdatedInput["command"] != "npm run lint" {
		t.Fatalf("permission allow: %+v", r)
	}
	if r := run(PermissionRequest, "bash", "echo no >&2; exit 2", nil); r.Decision != "" {
		t.Fatalf("permission exit 2: %+v", r)
	}
	r = run(PermissionRequest, "bash", `echo '{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"nope","interrupt":true}}}'`, nil)
	if r.Decision != "deny" || r.Reason != "nope" || !r.StopAll {
		t.Fatalf("permission deny: %+v", r)
	}
	// PermissionDenied retry.
	if r := run(PermissionDenied, "bash", `echo '{"hookSpecificOutput":{"hookEventName":"PermissionDenied","retry":true}}'`, nil); !r.Retry {
		t.Fatalf("retry: %+v", r)
	}
	// PreToolUse defer is treated as ask (OpenUAI is interactive).
	if r := run(PreToolUse, "bash", `echo '{"hookSpecificOutput":{"permissionDecision":"defer","additionalContext":"x"}}'`, nil); r.Decision != "ask" || len(r.Context) != 0 {
		t.Fatalf("defer: %+v", r)
	}
	// SessionStart: plain stdout is context, exit 2 shows stderr to the user only.
	r = run(SessionStart, "startup", `echo '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"branch main","initialUserMessage":"hello","sessionTitle":"T","watchPaths":["/tmp/a"]}}'`, map[string]any{"source": "startup"})
	if len(r.Context) != 1 || r.InitialUserMessage != "hello" || r.SessionTitle != "T" || !r.WatchPathsSet || r.WatchPaths[0] != "/tmp/a" {
		t.Fatalf("session start: %+v", r)
	}
	if r := run(SessionStart, "startup", "echo plain context", nil); len(r.Context) != 1 || r.Context[0] != "plain context" {
		t.Fatalf("session start plain: %+v", r)
	}
	if r := run(SessionStart, "clear", "echo oops >&2; exit 2", nil); r.Decision != "" || len(r.Notices) != 1 || len(r.Errors) != 0 {
		t.Fatalf("session start exit 2: %+v", r)
	}
	// PostToolBatch: exit 2 and decision block stop the loop.
	if r := run(PostToolBatch, "", "echo stop here >&2; exit 2", nil); r.Decision != "block" || r.Reason != "stop here" {
		t.Fatalf("batch: %+v", r)
	}
	// StopFailure ignores everything.
	if r := run(StopFailure, "rate_limit", `echo '{"continue":false,"systemMessage":"x"}'; exit 2`, nil); r.StopAll || len(r.Messages) != 0 || r.Decision != "" {
		t.Fatalf("stop failure: %+v", r)
	}
	// StopFailure uses the narrow matcher: a hyphen makes it a regexp.
	s := set(t, Config{StopFailure: {{Matcher: "rate_limit|server_error", Hooks: cmd("true")}}})
	if r := s.Run(context.Background(), StopFailure, "server_error", nil, dir); r.Ran != 1 {
		t.Fatal("narrow matcher list")
	}
	// MessageDisplay displayContent; continue:false is discarded.
	r = run(MessageDisplay, "", `echo '{"continue":false,"hookSpecificOutput":{"hookEventName":"MessageDisplay","displayContent":"plain"}}'`, nil)
	if r.DisplayContent == nil || *r.DisplayContent != "plain" || r.StopAll {
		t.Fatalf("display: %+v", r)
	}
	// PreModelSwitch: exit 2 denies; a timeout blocks too.
	if r := run(PreModelSwitch, "opus", "echo retired >&2; exit 2", nil); r.Decision != "deny" || r.Reason != "retired" {
		t.Fatalf("model switch: %+v", r)
	}
	slow := set(t, Config{PreModelSwitch: {{Hooks: []Handler{{Type: "command", Command: "sleep 3", Timeout: 1}}}}})
	if r := slow.Run(context.Background(), PreModelSwitch, "x", nil, dir); r.Decision != "deny" {
		t.Fatalf("model switch timeout: %+v", r)
	}
	// PostModelSwitch plain stdout is context.
	if r := run(PostModelSwitch, "opus", "echo use sub-agents", nil); len(r.Context) != 1 {
		t.Fatalf("post switch: %+v", r)
	}
	// WorktreeCreate: last non-empty stdout line is the path; failure blocks.
	wt := filepath.Join(dir, "wt")
	if r := run(WorktreeCreate, "", "printf 'banner\\n\\033[1m"+wt+"\\033[0m\\n\\n'", nil); r.WorktreePath != wt {
		t.Fatalf("worktree path: %+v", r)
	}
	if r := run(WorktreeCreate, "", "exit 1", nil); r.Decision != "block" {
		t.Fatalf("worktree fail: %+v", r)
	}
	// continue:false stops, with its reason.
	if r := run(PostToolUse, "bash", `echo '{"continue":false,"stopReason":"Build failed"}'`, nil); !r.StopAll || r.StopReason != "Build failed" {
		t.Fatalf("continue false: %+v", r)
	}
	// Stop: exit 2 with a missing file in stderr is a non-blocking error.
	if r := run(Stop, "", "/nonexistent/stop.sh", nil); r.Decision != "" {
		t.Fatalf("missing stop hook: %+v", r)
	}
	// UserPromptSubmit sessionTitle and suppressOriginalPrompt.
	r = run(UserPromptSubmit, "", `echo '{"decision":"block","reason":"no","hookSpecificOutput":{"sessionTitle":"S","suppressOriginalPrompt":true}}'`, nil)
	if r.Decision != "block" || r.SessionTitle != "S" || !r.SuppressPrompt {
		t.Fatalf("prompt submit: %+v", r)
	}
	// Several JSON lines that set fields: a parse failure.
	if r := run(PreToolUse, "bash", `echo '{"a":1}'; echo '{"decision":"block"}'`, nil); r.Decision != "" || len(r.Errors) != 1 {
		t.Fatalf("multi json: %+v", r)
	}
}

func TestSessionEndBudget(t *testing.T) {
	s := set(t, Config{SessionEnd: {{Hooks: cmd("sleep 5")}}})
	start := time.Now()
	s.Run(context.Background(), SessionEnd, "other", nil, t.TempDir())
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("SessionEnd must respect the 1.5 s budget, took %s", d)
	}
}

func TestLoaderConfigChange(t *testing.T) {
	cfgDir, work := t.TempDir(), t.TempDir()
	user := filepath.Join(cfgDir, "hooks.json")
	writeHooks(t, user, `{"hooks":{"ConfigChange":[{"matcher":"user_settings","hooks":[{"type":"command","command":"echo locked >&2; exit 2"}]}],"Stop":[{"hooks":[{"type":"command","command":"true"}]}]}}`)
	var calls []string
	l := Loader{OnChange: func(prev *Set, source, path string) bool {
		calls = append(calls, source)
		return prev.Run(context.Background(), ConfigChange, source, map[string]any{"source": source, "file_path": path}, work).Decision == "block"
	}}
	if s := l.Load(cfgDir, work); !s.Has(Stop) {
		t.Fatal("first load")
	}
	writeHooks(t, user, `{"hooks":{}}`)
	if s := l.Load(cfgDir, work); !s.Has(Stop) || len(calls) != 1 || calls[0] != "user_settings" {
		t.Fatalf("blocked change must keep the old hooks: %v %+v", calls, s)
	}
	l.Load(cfgDir, work)
	if len(calls) != 1 {
		t.Fatal("a blocked change is not re-checked until it changes again")
	}
	// A project file change passes (no hook for project_settings).
	proj := filepath.Join(work, ".openuai", "hooks.json")
	writeHooks(t, proj, `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"true"}]}]}}`)
	if s := l.Load(cfgDir, work); !s.Has(PreToolUse) {
		t.Fatal("project change must apply")
	}
}
