package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"openuai/internal/llm"
)

// RestartNotePrefix starts the note added to the conversation when OpenUAI
// comes back from a restart, so the UI can show it as a notice.
const RestartNotePrefix = "[OpenUAI restarted"

// maxResumeAttempts caps how many restarts in a row may resume the same turn,
// so a turn that keeps restarting the app cannot loop forever.
const maxResumeAttempts = 3

const interruptedToolResult = "Error: interrupted — OpenUAI restarted while this tool was running. " +
	"It may or may not have completed: check its effect before repeating it."

// Run adds userMessage to the conversation and runs the agent loop.
func (a *Agent) Run(ctx context.Context, userMessage string) error {
	a.Begin(userMessage)
	return a.Continue(ctx)
}

// Begin adds userMessage to the conversation and marks a turn as in progress,
// without calling the model yet. Callers save the session right after, so
// the message survives a restart that happens during the turn.
func (a *Agent) Begin(userMessage string) {
	a.BeginWithImages(userMessage, nil)
}

// BeginWithImages is Begin with images attached to the user's message
// (base64-encoded). They are saved with the conversation.
func (a *Agent) BeginWithImages(userMessage string, images []string) {
	a.BeginWithAttachments(userMessage, images, nil)
}

// Continue runs the agent loop on the current conversation.
func (a *Agent) Continue(ctx context.Context) error {
	a.runInProgress = true
	err := a.loop(ctx)
	a.runInProgress = false
	if err == nil {
		a.resumeAttempts = 0
	}
	return err
}

// Resume continues the turn a restart interrupted (see RecoverInterrupted).
func (a *Agent) Resume(ctx context.Context) error {
	a.resumePending = false
	return a.Continue(ctx)
}

// ResumePending reports whether a turn interrupted by a restart is waiting
// to be resumed.
func (a *Agent) ResumePending() bool { return a.resumePending }

// RunInProgress reports whether a turn is running (or was, when saved).
func (a *Agent) RunInProgress() bool { return a.runInProgress }

// RecoverInterrupted is called once after the session saved before a restart
// has been loaded. If a turn was in progress, tool calls left without result
// are closed and a note tells the agent to continue; killed lists the
// background processes that were running. It reports whether the turn should
// be resumed and whether the conversation changed (and must be saved).
func (a *Agent) RecoverInterrupted(killed []string, at time.Time) (resume, changed bool) {
	return a.RecoverInterruptedWithNote(killed, "", at)
}

// RecoverInterruptedWithNote is RecoverInterrupted with note (e.g. what
// happened to the background sub-agents) added to the restart note.
func (a *Agent) RecoverInterruptedWithNote(killed []string, note string, at time.Time) (resume, changed bool) {
	interrupted := a.runInProgress
	a.runInProgress = false
	if !interrupted && len(killed) == 0 && note == "" {
		return false, false
	}
	if interrupted {
		a.closeDanglingToolCalls()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s at %s]", RestartNotePrefix, at.Format("15:04"))
	if interrupted {
		if a.resumeAttempts < maxResumeAttempts {
			resume = true
			a.resumeAttempts++
			b.WriteString(" You were in the middle of the previous request when the app restarted. " +
				"Continue from where you left off: do not repeat steps that already completed " +
				"(check their effect if unsure), and do not request another restart unless the user asks for it.")
		} else {
			fmt.Fprintf(&b, " The previous request was interrupted by %d restarts in a row, so it was not resumed "+
				"again automatically. Tell the user where it stopped and wait for instructions.", a.resumeAttempts)
			a.resumeAttempts = 0
		}
	}
	if len(killed) > 0 {
		b.WriteString(" Background processes that were running before the restart (no longer reachable with the proc_* tools): " +
			strings.Join(killed, ", ") + ".")
	}
	if note != "" {
		b.WriteString(" " + note)
	}
	a.messages = append(a.messages, llm.Message{Role: llm.RoleUser, Content: b.String()})
	a.resumePending = resume
	return resume, true
}

// closeDanglingToolCalls adds a result to every tool call of the last
// assistant message that has none, so the conversation stays valid.
func (a *Agent) closeDanglingToolCalls() {
	last := -1
	for i := len(a.messages) - 1; i >= 0; i-- {
		m := a.messages[i]
		if m.Role == llm.RoleAssistant {
			if len(m.ToolCalls) > 0 {
				last = i
			}
			break
		}
		if m.Role == llm.RoleUser {
			break
		}
	}
	if last < 0 {
		return
	}
	have := map[string]bool{}
	for _, m := range a.messages[last+1:] {
		if m.Role == llm.RoleToolResult {
			have[m.ToolCallID] = true
		}
	}
	for _, tc := range a.messages[last].ToolCalls {
		if !have[tc.ID] {
			a.messages = append(a.messages, llm.Message{Role: llm.RoleToolResult, ToolCallID: tc.ID, Content: interruptedToolResult})
		}
	}
}
