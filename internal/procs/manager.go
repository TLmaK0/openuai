// Package procs runs interactive background processes inside a pseudo-terminal
// so that both the agent and the user can follow their output and type into
// them (prompts, wizards, dev servers, long builds...).
package procs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	gopty "github.com/aymanbagabas/go-pty"
)

// BufferSize is how much recent output is kept per process.
const BufferSize = 512 * 1024

const (
	DefaultCols = 120
	DefaultRows = 30

	// A process that has been silent this long and whose last line looks like
	// a prompt is considered to be waiting for input.
	idleBeforeWaiting = 1500 * time.Millisecond

	// maxAgentRead caps the raw output handed to the agent in one read.
	maxAgentRead = 16 * 1024
)

// Status of a background process.
type Status string

const (
	StatusRunning Status = "running"
	StatusWaiting Status = "waiting_for_input"
	StatusExited  Status = "exited"
)

// Info describes a process for the agent and the UI.
type Info struct {
	ID        string     `json:"id"`
	Command   string     `json:"command"`
	Cwd       string     `json:"cwd"`
	Status    Status     `json:"status"`
	ExitCode  int        `json:"exit_code"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Callbacks let the app forward process activity to the UI and the agent.
// They are called from the manager's goroutines and must not block.
type Callbacks struct {
	OnStart  func(Info)
	OnOutput func(id string, offset int64, data []byte)
	OnStatus func(Info)
	OnExit   func(Info)
}

// ErrNotFound is returned for unknown process IDs.
var ErrNotFound = errors.New("no such process")

// Proc is one background process attached to a pseudo-terminal.
type Proc struct {
	id, command, cwd string
	started          time.Time
	pty              gopty.Pty
	cmd              *gopty.Cmd
	done             chan struct{}

	mu            sync.Mutex
	buf           []byte // last BufferSize bytes of output
	total         int64  // bytes of output ever produced
	lastOut       time.Time
	waiting       bool
	exited        bool
	exitCode      int
	ended         time.Time
	agentOffset   int64 // where the agent's next read continues from
	killedByAgent bool
	exitSeen      bool // the agent already saw the exit through a read

	userLine []byte   // what the user is typing right now (never leaves the app)
	typed    []string // recent lines typed by the user, scrubbed from agent reads
}

// ID returns the process ID.
func (p *Proc) ID() string { return p.id }

// Done is closed once the process has exited and its output is drained.
func (p *Proc) Done() <-chan struct{} { return p.done }

// Info returns the current state of the process.
func (p *Proc) Info() Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.infoLocked()
}

func (p *Proc) infoLocked() Info {
	i := Info{ID: p.id, Command: p.command, Cwd: p.cwd, StartedAt: p.started, Status: StatusRunning}
	switch {
	case p.exited:
		i.Status = StatusExited
		i.ExitCode = p.exitCode
		e := p.ended
		i.EndedAt = &e
	case p.waiting:
		i.Status = StatusWaiting
	}
	return i
}

func (p *Proc) appendLocked(b []byte) {
	p.buf = append(p.buf, b...)
	p.total += int64(len(b))
	if len(p.buf) > BufferSize {
		nb := make([]byte, BufferSize)
		copy(nb, p.buf[len(p.buf)-BufferSize:])
		p.buf = nb
	}
}

// minTypedScrub is the shortest typed line that is scrubbed from agent reads;
// shorter ones ("y", "1") would mangle unrelated output.
const minTypedScrub = 4

const maxTyped = 32

func (p *Proc) trackTypedLocked(data []byte) {
	for _, b := range data {
		switch {
		case b == '\r' || b == '\n':
			line := strings.TrimSpace(ansiRe.ReplaceAllString(string(p.userLine), ""))
			p.userLine = p.userLine[:0]
			if len(line) >= minTypedScrub {
				p.typed = append(p.typed, line)
				if len(p.typed) > maxTyped {
					p.typed = p.typed[len(p.typed)-maxTyped:]
				}
			}
		case b == 0x7f || b == '\b':
			if n := len(p.userLine); n > 0 {
				p.userLine = p.userLine[:n-1]
			}
		case b == 0x03: // Ctrl-C abandons the line
			p.userLine = p.userLine[:0]
		default:
			p.userLine = append(p.userLine, b)
		}
	}
}

func scrubTyped(out string, typed []string) string {
	for _, t := range typed {
		out = strings.ReplaceAll(out, t, "[typed by user]")
	}
	return out
}

func (p *Proc) startOffsetLocked() int64 { return p.total - int64(len(p.buf)) }

// Manager owns every background process of the app.
type Manager struct {
	mu       sync.Mutex
	procs    map[string]*Proc
	seq      int
	cb       Callbacks
	stop     chan struct{}
	stopOnce sync.Once
}

// NewManager creates a manager and starts its idle watcher.
func NewManager(cb Callbacks) *Manager {
	m := &Manager{procs: map[string]*Proc{}, cb: cb, stop: make(chan struct{})}
	go m.watchIdle()
	return m
}

// Start runs command through the system shell inside a new pseudo-terminal.
// An empty cwd means the app's working directory.
func (m *Manager) Start(command, cwd string) (*Proc, error) {
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("command is required")
	}
	if cwd != "" {
		if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("cwd %q is not a directory", cwd)
		}
	}
	pt, err := gopty.New()
	if err != nil {
		return nil, fmt.Errorf("cannot open a pseudo-terminal: %w", err)
	}
	_ = pt.Resize(DefaultCols, DefaultRows)

	var c *gopty.Cmd
	if runtime.GOOS == "windows" {
		shell := os.Getenv("COMSPEC")
		if shell == "" {
			shell = "cmd.exe"
		}
		c = pt.Command(shell, "/C", command)
	} else {
		c = pt.Command("sh", "-c", command)
	}
	c.Dir = cwd
	c.Env = append(os.Environ(), "TERM=xterm-256color")
	if err := c.Start(); err != nil {
		_ = pt.Close()
		return nil, fmt.Errorf("cannot start %q: %w", command, err)
	}
	// The child has its own copy of the terminal; closing ours lets reads
	// end as soon as the process (and anything it spawned) exits.
	if up, ok := pt.(gopty.UnixPty); ok {
		_ = up.Slave().Close()
	}

	p := &Proc{
		command: command,
		cwd:     cwd,
		started: time.Now(),
		pty:     pt,
		cmd:     c,
		done:    make(chan struct{}),
		lastOut: time.Now(),
	}
	m.mu.Lock()
	m.seq++
	p.id = fmt.Sprintf("p%d", m.seq)
	m.procs[p.id] = p
	m.mu.Unlock()

	if m.cb.OnStart != nil {
		m.cb.OnStart(p.Info())
	}
	readDone := make(chan struct{})
	go m.readLoop(p, readDone)
	go m.waitLoop(p, readDone)
	return p, nil
}

func (m *Manager) readLoop(p *Proc, done chan struct{}) {
	defer close(done)
	b := make([]byte, 32*1024)
	for {
		n, err := p.pty.Read(b)
		if n > 0 {
			chunk := append([]byte(nil), b[:n]...)
			p.mu.Lock()
			off := p.total
			p.appendLocked(chunk)
			wasWaiting := p.waiting
			p.waiting = false
			p.lastOut = time.Now()
			p.mu.Unlock()
			if m.cb.OnOutput != nil {
				m.cb.OnOutput(p.id, off, chunk)
			}
			if wasWaiting {
				m.status(p)
			}
		}
		if err != nil {
			return
		}
	}
}

func (m *Manager) waitLoop(p *Proc, readDone chan struct{}) {
	err := p.cmd.Wait()
	code := 0
	if p.cmd.ProcessState != nil {
		code = p.cmd.ProcessState.ExitCode()
	} else if err != nil {
		code = -1
	}
	// Let the reader drain what is left; children that outlive the process
	// may keep the terminal open, so do not wait for them forever.
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
	}
	_ = p.pty.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
	}

	p.mu.Lock()
	p.exited = true
	p.exitCode = code
	p.waiting = false
	p.ended = time.Now()
	p.mu.Unlock()
	close(p.done)
	if m.cb.OnExit != nil {
		m.cb.OnExit(p.Info())
	}
}

func (m *Manager) status(p *Proc) {
	if m.cb.OnStatus != nil {
		m.cb.OnStatus(p.Info())
	}
}

func (m *Manager) watchIdle() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		for _, p := range m.all() {
			p.mu.Lock()
			flag := !p.exited && !p.waiting && p.total > 0 &&
				time.Since(p.lastOut) >= idleBeforeWaiting &&
				LooksLikePrompt(string(tail(p.buf, 2048)))
			if flag {
				p.waiting = true
			}
			p.mu.Unlock()
			if flag {
				m.status(p)
			}
		}
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

func (m *Manager) get(id string) (*Proc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return p, nil
}

func (m *Manager) all() []*Proc {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Proc, 0, len(m.procs))
	for _, p := range m.procs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].started.Before(out[j].started) })
	return out
}

// Get returns a process by ID.
func (m *Manager) Get(id string) (*Proc, error) { return m.get(id) }

// List returns every process, oldest first.
func (m *Manager) List() []Info {
	ps := m.all()
	out := make([]Info, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Info())
	}
	return out
}

// ReadResult is output prepared for the agent.
type ReadResult struct {
	Info
	Output    string
	From      int64
	Next      int64
	Truncated bool
}

// Read returns output for the agent, with terminal codes removed and secrets
// redacted. since < 0 continues from where the agent last read.
func (m *Manager) Read(id string, since int64) (ReadResult, error) {
	p, err := m.get(id)
	if err != nil {
		return ReadResult{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var r ReadResult
	start := p.startOffsetLocked()
	from := since
	if from < 0 {
		from = p.agentOffset
	}
	if from < start {
		from = start
		r.Truncated = true
	}
	if from > p.total {
		from = p.total
	}
	data := p.buf[from-start:]
	if len(data) > maxAgentRead {
		data = data[len(data)-maxAgentRead:]
		from = p.total - maxAgentRead
		r.Truncated = true
	}
	r.Output = Redact(scrubTyped(Clean(string(data)), p.typed))
	r.From, r.Next = from, p.total
	p.agentOffset = p.total
	if p.exited {
		p.exitSeen = true
	}
	r.Info = p.infoLocked()
	return r, nil
}

// Pending returns how many output bytes the agent has not read yet.
func (m *Manager) Pending(id string) int64 {
	p, err := m.get(id)
	if err != nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total - p.agentOffset
}

// Wait blocks until the process has output the agent has not read, is waiting
// for input, has exited, the timeout passes or ctx ends.
func (m *Manager) Wait(ctx context.Context, id string, timeout time.Duration) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		p.mu.Lock()
		ready := p.exited || p.waiting || p.total > p.agentOffset
		p.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return nil
		case <-tick.C:
		}
	}
}

// Idle returns how long the process has been silent.
func (m *Manager) Idle(id string) time.Duration {
	p, err := m.get(id)
	if err != nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Since(p.lastOut)
}

// Snapshot returns the raw buffered output (for the terminal view) and the
// total number of bytes produced so far.
func (m *Manager) Snapshot(id string) ([]byte, int64, error) {
	p, err := m.get(id)
	if err != nil {
		return nil, 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.buf...), p.total, nil
}

// Write sends input from the agent to the process as if typed on its terminal.
func (m *Manager) Write(id string, data []byte) error { return m.write(id, data, false) }

// WriteUser sends input typed by the user. What the user types (passwords,
// tokens...) must never reach the LLM, so it is remembered and scrubbed from
// the output the agent reads, in case the program echoes it back.
func (m *Manager) WriteUser(id string, data []byte) error { return m.write(id, data, true) }

func (m *Manager) write(id string, data []byte, user bool) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		return fmt.Errorf("process %s has exited", id)
	}
	if user {
		p.trackTypedLocked(data)
	}
	wasWaiting := p.waiting
	p.waiting = false
	p.lastOut = time.Now()
	p.mu.Unlock()
	if _, err := p.pty.Write(data); err != nil {
		return err
	}
	if wasWaiting {
		m.status(p)
	}
	return nil
}

// Resize changes the terminal size of the process.
func (m *Manager) Resize(id string, cols, rows int) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	if cols <= 0 || rows <= 0 {
		return errors.New("invalid size")
	}
	p.mu.Lock()
	exited := p.exited
	p.mu.Unlock()
	if exited {
		return nil
	}
	return p.pty.Resize(cols, rows)
}

// Kill stops the process and everything it started: first politely, then
// forcefully. byAgent marks kills requested by the agent, which then does not
// need to be told that the process ended.
func (m *Manager) Kill(id string, byAgent bool) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		return nil
	}
	if byAgent {
		p.killedByAgent = true
	}
	p.mu.Unlock()

	signalTree(p.cmd.Process, true)
	select {
	case <-p.done:
		return nil
	case <-time.After(2 * time.Second):
	}
	signalTree(p.cmd.Process, false)
	select {
	case <-p.done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("process %s did not exit", id)
	}
}

// ExitObserved reports whether the agent already knows the process ended
// (it read the exit status or killed it itself).
func (m *Manager) ExitObserved(id string) bool {
	p, err := m.get(id)
	if err != nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitSeen || p.killedByAgent
}

// Shutdown kills every running process. Used when the app quits.
func (m *Manager) Shutdown() {
	m.stopOnce.Do(func() { close(m.stop) })
	for _, p := range m.all() {
		p.mu.Lock()
		running := !p.exited
		p.mu.Unlock()
		if running {
			signalTree(p.cmd.Process, false)
		}
	}
}
