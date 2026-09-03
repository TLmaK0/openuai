package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"openuai/internal/llm"
)

// binary is the headless agent this plugin drives. It is looked up on PATH so
// that an installation anywhere works, and so a missing one is reported as a
// missing binary rather than a failed turn. OPENUAI_CLAUDE_BINARY overrides the
// lookup, for an installation that is not on PATH.
var binary = envOr("OPENUAI_CLAUDE_BINARY", "claude")

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// runCeiling bounds one turn. The core passes its caller's context to Chat,
// but a JSON-RPC call carries no cancellation, so the child cannot be told to
// stop from outside: the ceiling is what stops a turn holding a process for
// the life of the app. It is deliberately far above a long turn and far below
// forever.
const runCeiling = 10 * time.Minute

// event is one line of --output-format stream-json. Only the fields this
// plugin acts on are named; the rest of the stream is ignored on purpose, so
// a new event type is not a parse error.
type event struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	// init
	Model string   `json:"model"`
	Tools []string `json:"tools"`

	// api_retry
	Attempt     int    `json:"attempt"`
	MaxRetries  int    `json:"max_retries"`
	ErrorStatus int    `json:"error_status"`
	Error       string `json:"error"`

	// result
	Result           string          `json:"result"`
	IsError          bool            `json:"is_error"`
	TerminalReason   string          `json:"terminal_reason"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// outcome is what one headless run produced.
type outcome struct {
	Text             string
	StructuredOutput json.RawMessage
	Model            string
	InputTokens      int
	OutputTokens     int
	CostUSD          float64
}

// fatalRetry reports whether a retry category is one that retrying cannot
// fix. A bad credential is retried ten times over three minutes, and the core
// abandons the call long before that, so waiting for the process to give up
// turns a precise error into a silent dead provider. These are surfaced on the
// first occurrence instead; the rest — rate_limit, overloaded, server_error —
// are left to the retry that exists for them.
func fatalRetry(category string) bool {
	switch category {
	case "authentication_failed", "oauth_org_not_allowed", "billing_error",
		"invalid_request", "model_not_found":
		return true
	}
	return false
}

// retryMessage turns a category into something a settings screen can act on.
func retryMessage(category string, status int) string {
	switch category {
	case "authentication_failed":
		return "the headless agent rejected the credentials: run `claude auth login`, " +
			"or set an API key for this provider"
	case "oauth_org_not_allowed":
		return "the logged-in account's organisation does not allow this use; " +
			"use an API key for this provider instead"
	case "billing_error":
		return "the account behind the headless agent has a billing problem"
	case "model_not_found":
		return "the headless agent does not serve the selected model"
	case "invalid_request":
		return fmt.Sprintf("the headless agent refused the request (HTTP %d)", status)
	}
	return fmt.Sprintf("the headless agent failed: %s (HTTP %d)", category, status)
}

// invocation is one headless call, assembled rather than formatted into a
// string, so nothing here goes near a shell.
type invocation struct {
	model      string
	system     string
	prompt     string
	jsonSchema string
	// apiKey is empty on the prior-login path, which is the ordinary one: the
	// user authenticated with their own agent before openuai ever ran.
	apiKey string
}

// args builds the command line.
//
// --tools "" leaves the agent no tools of its own, which is what makes this a
// model backend rather than a second agent: openuai keeps its loop, its tools
// and its permission prompts, and only the tokens come from here.
//
// --restricted is not about tools here, since there are none. It is what stops
// the run reading the working directory's settings: without it a -p session
// runs that directory's hooks and connects its MCP servers with no trust
// prompt, and the working directory belongs to whatever openuai was started
// in.
//
// --bare is added only with an API key. It also skips host configuration, but
// it never reads OAuth credentials or the keychain, so on the prior-login path
// it would authenticate nothing.
func (in invocation) args() []string {
	args := []string{
		"-p", in.prompt,
		"--output-format", "stream-json",
		// stream-json refuses to run without it, and it is what carries the
		// api_retry events this plugin reports errors from.
		"--verbose",
		"--tools", "",
		"--restricted",
		"--strict-mcp-config",
	}
	if in.apiKey != "" {
		args = append(args, "--bare")
	}
	if in.model != "" {
		args = append(args, "--model", in.model)
	}
	if in.system != "" {
		args = append(args, "--system-prompt", in.system)
	}
	if in.jsonSchema != "" {
		args = append(args, "--json-schema", in.jsonSchema)
	}
	return args
}

// run executes one turn and returns what it produced.
func (in invocation) run(ctx context.Context) (*outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, runCeiling)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, in.args()...)
	if in.apiKey != "" {
		// Only on this path: appended to the inherited environment, so the
		// child keeps HOME and PATH and can still find its own installation.
		cmd.Env = append(cmd.Environ(), "ANTHROPIC_API_KEY="+in.apiKey)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, notInstalled(err)
	}

	out, parseErr := parseStream(stdout)
	// The stream is drained before waiting, so a child writing more than a
	// pipe holds cannot block in write(2) while this waits on it.
	waitErr := cmd.Wait()

	if parseErr != nil {
		return nil, parseErr
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			// An argument the installed version does not know lands here, and
			// naming it is more use than the exit code.
			return nil, fmt.Errorf("%s", firstLine(msg))
		}
		return nil, fmt.Errorf("the headless agent exited: %w", waitErr)
	}
	if out == nil {
		return nil, fmt.Errorf("the headless agent produced no result")
	}
	return out, nil
}

// parseStream reads the event stream, stopping early on a failure that
// retrying cannot fix.
func parseStream(r io.Reader) (*outcome, error) {
	out := &outcome{}
	scanner := bufio.NewScanner(r)
	// A turn's result line carries the whole reply, which outgrows the default
	// 64 KiB buffer.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			// Not every line has to be an event this plugin understands.
			continue
		}

		switch {
		case ev.Type == "system" && ev.Subtype == "init":
			// The alias asked for resolves to a model id here, which is what
			// gets reported back so usage is costed against a real name.
			out.Model = ev.Model

		case ev.Type == "system" && ev.Subtype == "api_retry":
			if fatalRetry(ev.Error) {
				return nil, fmt.Errorf("%s", retryMessage(ev.Error, ev.ErrorStatus))
			}

		case ev.Type == "result":
			out.Text = ev.Result
			out.StructuredOutput = ev.StructuredOutput
			out.InputTokens = ev.Usage.InputTokens
			out.OutputTokens = ev.Usage.OutputTokens
			out.CostUSD = ev.TotalCostUSD
			if ev.IsError {
				if ev.Result != "" {
					return nil, fmt.Errorf("%s", firstLine(ev.Result))
				}
				return nil, fmt.Errorf("the headless agent failed: %s", ev.TerminalReason)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading the headless agent's output: %w", err)
	}
	if out.Text == "" && len(out.StructuredOutput) == 0 {
		return nil, nil
	}
	return out, nil
}

// response turns an outcome into the contract type the core costs and renders.
func (o *outcome) response(fallbackModel string) *llm.Response {
	model := o.Model
	if model == "" {
		model = fallbackModel
	}
	return &llm.Response{
		Content:      o.Text,
		InputTokens:  o.InputTokens,
		OutputTokens: o.OutputTokens,
		Model:        model,
	}
}

func notInstalled(err error) error {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return fmt.Errorf("%s is not installed or not on PATH", binary)
	}
	return err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
