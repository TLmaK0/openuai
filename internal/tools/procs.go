package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"openuai/internal/procs"
)

// Background processes run in a pseudo-terminal that the user can also see and
// type into. They share one manager, owned by the app.

const (
	procStartSettle = 1500 * time.Millisecond
	procMaxWait     = 60
)

// namedKeys are the keys proc_write can send besides literal text.
var namedKeys = map[string]string{
	"enter": "\r", "return": "\r", "tab": "\t", "esc": "\x1b", "escape": "\x1b",
	"backspace": "\x7f", "space": " ",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"home": "\x1b[H", "end": "\x1b[F",
	"ctrl-c": "\x03", "ctrl-d": "\x04", "ctrl-z": "\x1a", "ctrl-l": "\x0c",
}

func formatProcState(i procs.Info) string {
	switch i.Status {
	case procs.StatusExited:
		return fmt.Sprintf("exited (code %d)", i.ExitCode)
	case procs.StatusWaiting:
		return "waiting for input"
	}
	return "running"
}

func formatProcRead(r procs.ReadResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[process %s: %s; output offset %d..%d]", r.ID, formatProcState(r.Info), r.From, r.Next)
	if r.Truncated {
		b.WriteString("\n[earlier output was dropped]")
	}
	if strings.TrimSpace(r.Output) == "" {
		b.WriteString("\n(no new output)")
	} else {
		b.WriteString("\n" + r.Output)
	}
	return b.String()
}

// ProcStart starts a background process in a PTY.
type ProcStart struct{ M *procs.Manager }

func (t ProcStart) Definition() Definition {
	return Definition{
		Name: "proc_start",
		Description: "Start a command in the background inside a real terminal (PTY) and return its id. " +
			"Use it for interactive or long-running commands: logins (gh auth login, npm login), ssh/sudo prompts, " +
			"Y/n wizards, dev servers, watchers, long builds. The user sees a live terminal in the chat and can type into it " +
			"(passwords and tokens they type are never shown to you). Follow up with proc_read, proc_write and proc_kill. When it ends without you reading its exit, you get a notification starting with \"[Background process\".",
		Parameters: []Parameter{
			{Name: "command", Type: "string", Description: "The shell command to run", Required: true},
			{Name: "cwd", Type: "string", Description: "Working directory (default: the app's)", Required: false},
		},
		RequiresPermission: "session",
	}
}

func (t ProcStart) Execute(ctx context.Context, args map[string]string) Result {
	p, err := t.M.Start(args["command"], args["cwd"])
	if err != nil {
		return Result{Error: err.Error()}
	}
	// Give the program a moment to print its first prompt.
	wctx, cancel := context.WithTimeout(ctx, procStartSettle)
	defer cancel()
	_ = t.M.Wait(wctx, p.ID(), procStartSettle)
	r, _ := t.M.Read(p.ID(), -1)
	return Result{Output: fmt.Sprintf("Started process %s. The user can see and type into its terminal.\n%s", p.ID(), formatProcRead(r))}
}

// ProcRead returns new output of a background process.
type ProcRead struct{ M *procs.Manager }

func (t ProcRead) Definition() Definition {
	return Definition{
		Name: "proc_read",
		Description: "Read the new output of a background process and its state (running, waiting for input, exited with a code). " +
			"Set wait to block up to that many seconds until there is new output, the process waits for input or it exits.",
		Parameters: []Parameter{
			{Name: "id", Type: "string", Description: "Process id from proc_start", Required: true},
			{Name: "since_offset", Type: "string", Description: "Re-read from this output offset instead of continuing where the last read stopped", Required: false},
			{Name: "wait", Type: "string", Description: "Seconds to wait for new output (default 0, max 60)", Required: false},
		},
		RequiresPermission: "none",
	}
}

func (t ProcRead) Execute(ctx context.Context, args map[string]string) Result {
	id := args["id"]
	since := int64(-1)
	if s := args["since_offset"]; s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v < 0 {
			return Result{Error: "since_offset must be a non-negative integer"}
		}
		since = v
	}
	if w := args["wait"]; w != "" {
		secs, err := strconv.Atoi(w)
		if err != nil || secs < 0 {
			return Result{Error: "wait must be a non-negative number of seconds"}
		}
		if secs > procMaxWait {
			secs = procMaxWait
		}
		if since < 0 && secs > 0 {
			if err := t.M.Wait(ctx, id, time.Duration(secs)*time.Second); err != nil && ctx.Err() == nil {
				return Result{Error: err.Error()}
			}
		}
	}
	r, err := t.M.Read(id, since)
	if err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: formatProcRead(r)}
}

// ProcWrite sends text or keys to a background process.
type ProcWrite struct{ M *procs.Manager }

func (t ProcWrite) Definition() Definition {
	return Definition{
		Name: "proc_write",
		Description: "Send input to a background process. 'input' is typed literally (no Enter is added); " +
			"'keys' is a comma-separated list sent after it: enter, tab, esc, space, backspace, up, down, left, right, home, end, ctrl-c, ctrl-d, ctrl-z, ctrl-l. " +
			"Example: input=\"y\", keys=\"enter\". Never type passwords or tokens yourself; ask the user to type them in the terminal.",
		Parameters: []Parameter{
			{Name: "id", Type: "string", Description: "Process id from proc_start", Required: true},
			{Name: "input", Type: "string", Description: "Text to type", Required: false},
			{Name: "keys", Type: "string", Description: "Comma-separated special keys to send after the text", Required: false},
		},
		RequiresPermission: "session",
	}
}

func (t ProcWrite) Execute(_ context.Context, args map[string]string) Result {
	data := args["input"]
	for _, k := range strings.Split(args["keys"], ",") {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		seq, ok := namedKeys[k]
		if !ok {
			return Result{Error: fmt.Sprintf("unknown key %q", k)}
		}
		data += seq
	}
	if data == "" {
		return Result{Error: "nothing to send: give input and/or keys"}
	}
	if err := t.M.Write(args["id"], []byte(data)); err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: "sent; use proc_read (with wait) to see the result"}
}

// ProcKill stops a background process and everything it started.
type ProcKill struct{ M *procs.Manager }

func (t ProcKill) Definition() Definition {
	return Definition{
		Name:        "proc_kill",
		Description: "Stop a background process and the processes it started.",
		Parameters: []Parameter{
			{Name: "id", Type: "string", Description: "Process id from proc_start", Required: true},
		},
		RequiresPermission: "none",
	}
}

func (t ProcKill) Execute(_ context.Context, args map[string]string) Result {
	if err := t.M.Kill(args["id"], true); err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: "process " + args["id"] + " stopped"}
}

// ProcList lists background processes.
type ProcList struct{ M *procs.Manager }

func (t ProcList) Definition() Definition {
	return Definition{
		Name:               "proc_list",
		Description:        "List the background processes and their state.",
		RequiresPermission: "none",
	}
}

func (t ProcList) Execute(_ context.Context, _ map[string]string) Result {
	list := t.M.List()
	if len(list) == 0 {
		return Result{Output: "no background processes"}
	}
	var b strings.Builder
	for _, i := range list {
		fmt.Fprintf(&b, "%s  %s  %s\n", i.ID, formatProcState(i), i.Command)
	}
	return Result{Output: strings.TrimRight(b.String(), "\n")}
}
