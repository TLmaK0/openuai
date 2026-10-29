package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"openuai/internal/logger"
)

// projectDir is the value of OPENUAI_PROJECT_DIR.
func projectDir(dir string) string {
	if p := getEnv().ProjectDir; p != "" {
		return p
	}
	return dir
}

// runDir is where a command hook runs: dir, or the first of the project
// root, the home directory and the temp directory that still exists.
func runDir(dir string) string {
	cands := []string{dir, getEnv().ProjectDir}
	if h, err := os.UserHomeDir(); err == nil {
		cands = append(cands, h)
	}
	cands = append(cands, os.TempDir())
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			if c != dir && dir != "" {
				logger.Info("Hook: %s no longer exists, running from %s", dir, c)
			}
			return c
		}
	}
	return ""
}

const projectPlaceholder = "${OPENUAI_PROJECT_DIR}"

// buildCmd builds the process of a command hook: exec form when args is set
// (no shell; placeholders substituted as plain strings), shell form
// otherwise (sh -c on macOS/Linux, Git Bash or PowerShell on Windows).
func buildCmd(ctx context.Context, h Handler, event, dir string) *exec.Cmd {
	proj := projectDir(dir)
	sub := func(s string) string { return strings.ReplaceAll(s, projectPlaceholder, proj) }
	var cmd *exec.Cmd
	if h.Args != nil {
		args := make([]string, len(h.Args))
		for i, a := range h.Args {
			args[i] = sub(a)
		}
		cmd = exec.CommandContext(ctx, sub(h.Command), args...)
	} else {
		shell := h.Shell
		if runtime.GOOS == "windows" && shell == "" {
			shell = "powershell"
			if _, err := exec.LookPath("bash"); err == nil {
				shell = "bash"
			}
		}
		switch {
		case shell == "powershell":
			ps := "pwsh"
			if _, err := exec.LookPath(ps); err != nil {
				ps = "powershell"
			}
			cmd = exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command", sub(h.Command))
		case runtime.GOOS == "windows":
			cmd = exec.CommandContext(ctx, "bash", "-c", h.Command)
		default:
			cmd = exec.CommandContext(ctx, "sh", "-c", h.Command)
		}
	}
	cmd.Dir = runDir(dir)
	cmd.Env = append(os.Environ(), "OPENUAI_PROJECT_DIR="+proj, "OPENUAI_HOOK_EVENT="+event)
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// execResult is the raw result of running a command hook.
type execResult struct {
	code      int
	out, err  string
	startErr  string // could not start
	timedOut  bool
	cancelled bool
	dir       string
}

func execCommand(parent context.Context, h Handler, event string, stdin []byte, dir string, timeout time.Duration) execResult {
	ctx := parent
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, timeout)
		defer cancel()
	}
	cmd := buildCmd(ctx, h, event, dir)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := execResult{out: stdout.String(), err: strings.TrimSpace(stderr.String()), dir: cmd.Dir}
	if err != nil {
		var ee *exec.ExitError
		switch {
		case ctx.Err() == context.DeadlineExceeded && parent.Err() == nil:
			r.timedOut = true
		case parent.Err() != nil:
			r.cancelled = true
		case errors.As(err, &ee):
			r.code = ee.ExitCode()
		default:
			r.startErr = err.Error()
		}
	}
	return r
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

func runCommand(ctx context.Context, event, name string, e Entry, stdin []byte, dir string, timeout time.Duration) outcome {
	oc := outcome{name: name}
	x := execCommand(ctx, e.H, event, stdin, dir, timeout)
	switch {
	case x.cancelled:
		oc.cancelled = true
		return oc
	case x.timedOut:
		oc.failure, oc.timedOut = fmt.Sprintf("timed out after %s", timeout), true
		return oc
	case x.startErr != "":
		oc.failure = "could not run: " + x.startErr
		return oc
	}
	switch event {
	case WorktreeCreate:
		// The path is the last non-empty line of stdout; any non-zero exit
		// code fails the creation.
		if x.code != 0 {
			oc.failure = fmt.Sprintf("failed (exit %d): %s", x.code, firstLine(x.err))
			return oc
		}
		lines := strings.Split(ansi.ReplaceAllString(x.out, ""), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if l := strings.TrimSpace(lines[i]); l != "" {
				if !filepath.IsAbs(l) {
					l = filepath.Join(x.dir, l)
				}
				oc.path = filepath.Clean(l)
				break
			}
		}
		if oc.path == "" {
			oc.failure = "printed no worktree path"
		}
		return oc
	case WorktreeRemove:
		if x.code != 0 {
			oc.failure = fmt.Sprintf("failed (exit %d): %s", x.code, firstLine(x.err))
		}
		return oc
	}
	parsed, plain, perr := parseStdout(x.out)
	switch x.code {
	case 0:
		if perr != "" {
			oc.failure = perr
		} else {
			oc.parsed, oc.plain = parsed, plain
		}
	case 2:
		if (event == Stop || event == SubagentStop) && strings.TrimSpace(x.out) == "" && strings.Contains(x.err, "No such file or directory") {
			oc.failure = "failed (exit 2): " + firstLine(x.err)
			return oc
		}
		oc.block = true
		oc.blockMsg = x.err
		if oc.blockMsg == "" {
			oc.blockMsg = "blocked by " + name
		}
		oc.parsed = parsed
	default:
		switch {
		case parsed != nil && e.H.OnFailure != "block":
			oc.parsed = parsed // valid JSON decides, whatever the exit code
		case perr != "":
			oc.failure = perr
		default:
			oc.failure = fmt.Sprintf("failed with non-blocking status code (exit %d): %s", x.code, firstLine(x.err))
		}
	}
	return oc
}

// startAsync runs a command hook in the background. async: its
// additionalContext and systemMessage reach the model on the next turn.
// asyncRewake: exit code 2 wakes the agent at once with stderr (or stdout).
func startAsync(event, name string, e Entry, stdin []byte, dir string, timeout time.Duration) {
	go func() {
		t := time.Duration(0) // timeout is not enforced on async hooks
		if e.H.AsyncRewake {
			t = timeout
		}
		x := execCommand(context.Background(), e.H, event, stdin, dir, t)
		deliver := getEnv().Deliver
		switch {
		case x.timedOut || x.startErr != "":
			logger.Error("Background hook %s failed: timed out=%v %s", name, x.timedOut, x.startErr)
			return
		case e.H.AsyncRewake && x.code == 2:
			msg := x.err
			if msg == "" {
				msg = strings.TrimSpace(x.out)
			}
			logger.Info("Background hook %s exited 2: waking the agent", name)
			if deliver != nil {
				deliver(fmt.Sprintf("[Background hook %s exited with code 2]\n%s", name, escapeReminder(capText(msg))), true)
			}
			return
		}
		o := parseAsyncOutput(x.out)
		if o == nil {
			logger.Info("Background hook %s finished (exit %d)", name, x.code)
			return
		}
		var parts []string
		if c := o.HookSpecificOutput.AdditionalContext; c != "" {
			parts = append(parts, escapeReminder(capText(c)))
		}
		if m := o.SystemMessage; m != "" {
			parts = append(parts, escapeReminder(capText(m)))
		}
		logger.Info("Background hook %s finished (exit %d): %d notes for the model", name, x.code, len(parts))
		if len(parts) > 0 && deliver != nil {
			deliver(fmt.Sprintf("[Background hook %s]\n%s", name, strings.Join(parts, "\n")), false)
		}
	}()
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// interpolate resolves $VAR and ${VAR} in an HTTP header value, only for
// the variables listed in allowed; others become empty.
func interpolate(v string, allowed []string) string {
	return envRef.ReplaceAllStringFunc(v, func(m string) string {
		sm := envRef.FindStringSubmatch(m)
		name := sm[1]
		if name == "" {
			name = sm[2]
		}
		if contains(allowed, name) {
			return os.Getenv(name)
		}
		return ""
	})
}

func runHTTP(parent context.Context, name string, e Entry, body []byte, timeout time.Duration) outcome {
	oc := outcome{name: name}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.H.URL, bytes.NewReader(body))
	if err != nil {
		oc.failure = "invalid request: " + err.Error()
		return oc
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.H.Headers {
		req.Header.Set(k, interpolate(v, e.H.AllowedEnvVars))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		switch {
		case parent.Err() != nil:
			oc.cancelled = true
		case ctx.Err() == context.DeadlineExceeded:
			oc.failure, oc.timedOut = fmt.Sprintf("timed out after %s", timeout), true
		default:
			oc.failure = "connection failed: " + err.Error()
		}
		return oc
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			oc.failure, oc.timedOut = fmt.Sprintf("timed out after %s", timeout), true
		} else {
			oc.failure = "reading the response failed: " + err.Error()
		}
		return oc
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		oc.failure = fmt.Sprintf("returned HTTP %d", resp.StatusCode)
		return oc
	}
	t := strings.TrimSpace(string(data))
	if t == "" {
		return oc
	}
	if !strings.HasPrefix(t, "{") || !strings.HasSuffix(t, "}") {
		oc.failure = "returned a body that is not a JSON object"
		return oc
	}
	var o output
	if err := json.Unmarshal([]byte(t), &o); err != nil {
		oc.failure = "returned invalid JSON: " + err.Error()
		return oc
	}
	oc.parsed = &o
	return oc
}

var inputRef = regexp.MustCompile(`\$\{([^}]+)\}`)

// lookupPath finds a dotted path (tool_input.path) in the hook input.
func lookupPath(input map[string]any, path string) (any, bool) {
	var cur any = input
	for _, p := range strings.Split(strings.TrimSpace(path), ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// substitute replaces ${path} in the string values of v with the value at
// that path of the hook input (a string as is, anything else as JSON; a
// missing path becomes empty).
func substitute(v any, input map[string]any) any {
	switch t := v.(type) {
	case string:
		if m := inputRef.FindStringSubmatch(t); m != nil && m[0] == t {
			if val, ok := lookupPath(input, m[1]); ok {
				if _, isStr := val.(string); !isStr {
					return val // the whole value is one reference: keep its type
				}
			}
		}
		return inputRef.ReplaceAllStringFunc(t, func(ref string) string {
			val, ok := lookupPath(input, inputRef.FindStringSubmatch(ref)[1])
			if !ok || val == nil {
				return ""
			}
			if s, ok := val.(string); ok {
				return s
			}
			b, _ := json.Marshal(val)
			return string(b)
		})
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = substitute(x, input)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = substitute(x, input)
		}
		return out
	}
	return v
}

func runMCP(parent context.Context, name string, e Entry, input map[string]any, timeout time.Duration) outcome {
	oc := outcome{name: name}
	call := getEnv().MCP
	if call == nil {
		oc.failure = "cannot run: no MCP servers are available"
		return oc
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	args, _ := substitute(e.H.Input, input).(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	text, isErr, err := call(ctx, e.H.Server, e.H.Tool, args)
	switch {
	case err != nil && parent.Err() != nil:
		oc.cancelled = true
		return oc
	case err != nil && ctx.Err() == context.DeadlineExceeded:
		oc.failure, oc.timedOut = fmt.Sprintf("timed out after %s", timeout), true
		return oc
	case err != nil:
		oc.failure = "call failed: " + err.Error()
		return oc
	case isErr:
		oc.failure = "tool returned an error: " + firstLine(text)
		return oc
	}
	parsed, plain, perr := parseStdout(text)
	if perr != "" {
		oc.failure = perr
		return oc
	}
	oc.parsed, oc.plain = parsed, plain
	return oc
}

const promptSystem = `You evaluate a hook condition for OpenUAI, an AI assistant. You get an instruction and the hook input (JSON) describing what the assistant is doing.
Answer with ONLY a JSON object, no other text:
{"ok": true} when the action may proceed (the condition holds), or
{"ok": false, "reason": "short explanation"} when it must not.
Add "impossible": true together with "ok": false only when the condition can never be satisfied.`

const agentSystem = `You verify a hook condition for OpenUAI, an AI assistant. You get an instruction and the hook input (JSON). You can use read-only tools to inspect files and the repository.
When you are done, answer with ONLY a JSON object, no other text:
{"ok": true} when the condition holds, or {"ok": false, "reason": "short explanation"} when it does not.`

// expandArguments puts the input JSON in place of $ARGUMENTS (appending it
// when there is none); \$ is a literal $.
func expandArguments(prompt, input string) string {
	var b strings.Builder
	found := false
	for i := 0; i < len(prompt); i++ {
		switch {
		case prompt[i] == '\\' && i+1 < len(prompt) && prompt[i+1] == '$':
			b.WriteByte('$')
			i++
			if strings.HasPrefix(prompt[i+1:], "ARGUMENTS") {
				b.WriteString("ARGUMENTS")
				i += len("ARGUMENTS")
			}
		case strings.HasPrefix(prompt[i:], "$ARGUMENTS"):
			b.WriteString(input)
			found = true
			i += len("$ARGUMENTS") - 1
		default:
			b.WriteByte(prompt[i])
		}
	}
	if !found {
		b.WriteString("\n\nHook input:\n" + input)
	}
	return b.String()
}

// llmDecision is the answer of a prompt or agent hook.
type llmDecision struct {
	OK         *bool  `json:"ok"`
	Reason     string `json:"reason"`
	Impossible bool   `json:"impossible"`
}

func parseDecision(text string) (*llmDecision, error) {
	t := strings.TrimSpace(text)
	i, j := strings.Index(t, "{"), strings.LastIndex(t, "}")
	if i < 0 || j < i {
		return nil, errors.New("the model did not answer with a JSON object")
	}
	var d llmDecision
	if err := json.Unmarshal([]byte(t[i:j+1]), &d); err != nil {
		return nil, fmt.Errorf("the model answered invalid JSON: %v", err)
	}
	if d.OK == nil {
		return nil, errors.New(`the model's answer has no "ok" field`)
	}
	return &d, nil
}

func runLLM(parent context.Context, event, name string, e Entry, stdin []byte, dir string, timeout time.Duration) Result {
	var r Result
	ev := getEnv()
	isAgent := e.H.Type == "agent"
	if (isAgent && ev.Agent == nil) || (!isAgent && ev.Prompt == nil) {
		r.Errors = append(r.Errors, name+" cannot run: no model is available")
		return r
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	user := expandArguments(e.H.Prompt, string(stdin))
	var text string
	var err error
	if isAgent {
		text, err = ev.Agent(ctx, e.H.Model, agentSystem, user, runDir(dir))
	} else {
		text, err = ev.Prompt(ctx, e.H.Model, promptSystem, user)
	}
	if err != nil {
		switch {
		case parent.Err() != nil:
		case ctx.Err() == context.DeadlineExceeded:
			r.Errors = append(r.Errors, fmt.Sprintf("%s timed out after %s", name, timeout))
		default:
			r.Errors = append(r.Errors, name+" failed: "+err.Error())
		}
		return r
	}
	d, err := parseDecision(text)
	if err != nil {
		r.Errors = append(r.Errors, name+" failed: "+err.Error())
		return r
	}
	if *d.OK {
		r.ok = true
		return r
	}
	reason := capText(strings.TrimSpace(d.Reason))
	if reason == "" {
		reason = "the condition of " + name + " is not met"
	}
	// Agent hooks behave like prompt hooks with continueOnBlock: true.
	cont := e.H.ContinueOnBlock || isAgent
	switch event {
	case Stop, SubagentStop:
		if d.Impossible && !isAgent {
			return r // the condition can never hold: let the turn end
		}
		r.Decision, r.Reason = "block", reason
	case PreToolUse:
		r.Decision, r.Reason = "deny", reason
		if !cont {
			r.StopAll, r.StopReason = true, reason
		}
	case PostToolUse:
		if cont {
			r.Decision, r.Reason = "block", reason
		} else {
			r.StopAll, r.StopReason = true, reason
		}
	case PostToolBatch, UserPromptSubmit, UserPromptExpansion, PostToolUseFailure:
		r.Decision, r.Reason = "block", reason
	}
	// PermissionRequest and PermissionDenied: ok: false has no effect.
	return r
}
