package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"openuai/internal/hooks"
	"openuai/internal/llm"
	"openuai/internal/logger"
)

// HookContextMarker separates what the user wrote from the context that
// UserPromptSubmit hooks added (only the model needs to read it).
const HookContextMarker = "\n\n[Context from hooks]\n"

// WithHookContext appends hook-provided context to a user message.
func WithHookContext(text string, notes []string) string {
	if len(notes) == 0 {
		return text
	}
	return text + HookContextMarker + strings.Join(notes, "\n")
}

// maxStopContinuations caps how many times in a row Stop hooks can keep
// the agent going (same cap as Claude Code).
const maxStopContinuations = 8

func (a *Agent) hookSet() *hooks.Set {
	if a.hooks == nil {
		return nil
	}
	return a.hooks()
}

// SessionID identifies the conversation to hooks.
func (a *Agent) SessionID() string {
	if a.sessionID != "" {
		return a.sessionID
	}
	return "openuai"
}

// runHook runs the hooks of event and reports failures to the user (as an
// error step) and in the log. The caller decides what to tell the model.
// A hook that returns continue: false stops the turn (see haltTurn).
func (a *Agent) runHook(ctx context.Context, event, target string, in map[string]any) hooks.Result {
	s := a.hookSet()
	if !s.Has(event) {
		return hooks.Result{}
	}
	if in == nil {
		in = map[string]any{}
	}
	in["session_id"] = a.SessionID()
	if a.transcript != "" {
		in["transcript_path"] = a.transcript
	}
	mode := a.permMode
	if mode == "" {
		mode = "default"
	}
	in["permission_mode"] = mode
	if a.effort != "" {
		in["effort"] = map[string]any{"level": a.effort}
	}
	if a.agentType != "" {
		in["agent_type"] = a.agentType
		in["agent_id"] = a.agentID
	}
	ctx = hooks.WithStatus(ctx, func(msg string) { a.emit(StepResult{Type: "notice", Content: msg}) })
	r := s.Run(ctx, event, target, in, a.workDir)
	if r.Ran > 0 {
		logger.Info("Hooks %s(%s): ran=%d decision=%q context=%d errors=%d stop=%v", event, target, r.Ran, r.Decision, len(r.Context), len(r.Errors), r.StopAll)
	}
	for _, e := range r.Errors {
		logger.Error("Hook error: %s", e)
		a.emit(StepResult{Type: "error", Content: "Hook error: " + e})
	}
	for _, e := range r.Notices {
		logger.Info("Hook notice (%s): %s", event, e)
		a.emit(StepResult{Type: "error", Content: event + " hook error: " + e})
	}
	for _, m := range r.Messages {
		logger.Info("Hook message: %s", m)
		a.emit(StepResult{Type: "notice", Content: "Hook: " + m})
	}
	if r.StopAll && a.halt == "" {
		a.halt = r.StopReason
		if a.halt == "" {
			a.halt = event + " hook asked to stop"
		}
	}
	return r
}

// haltTurn ends the turn because a hook returned continue: false. The stop
// reason is shown to the user and stays in the conversation.
func (a *Agent) haltTurn() error {
	reason := a.halt
	a.halt = ""
	logger.Info("Turn stopped by a hook: %s", reason)
	a.messages = append(a.messages, llm.Message{Role: llm.RoleAssistant, Content: "[Stopped by a hook] " + reason})
	a.emit(StepResult{Type: "error", Content: "Stopped by a hook: " + reason})
	a.emit(StepResult{Type: "done", Content: ""})
	a.saveCheckpoint()
	return nil
}

// hookNotes is what the model is told about a hook run: added context and
// any hook that failed.
func hookNotes(r hooks.Result) []string {
	notes := append([]string(nil), r.Context...)
	for _, e := range r.Errors {
		notes = append(notes, "Hook error: "+e)
	}
	return notes
}

func withNotes(content string, notes []string) string {
	if len(notes) == 0 {
		return content
	}
	return content + "\n\n[Hooks]\n" + strings.Join(notes, "\n")
}

func argsAny(args map[string]string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

func argsString(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
		default:
			b, _ := json.Marshal(t)
			out[k] = string(b)
		}
	}
	return out
}

// toolAllowed applies a PreToolUse decision before the normal permission
// flow: "allow" skips the prompt (except in plan mode), "ask" forces it.
func (a *Agent) toolAllowed(ctx context.Context, pre hooks.Result, name, required, desc string, args map[string]string) permDecision {
	if pre.Decision == "allow" && a.permMode != PermModePlan {
		return permDecision{ok: true, byHook: true}
	}
	return a.decidePermission(ctx, name, required, desc, args, pre.Decision == "ask")
}

// postToolHooks runs PostToolUse / PostToolUseFailure and returns the tool
// result the model sees: replaced by updatedToolOutput, with what the hooks
// want the model to know appended.
func (a *Agent) postToolHooks(ctx context.Context, tc llm.ToolCall, args map[string]string, output, errText, content string, notes []string, dur time.Duration) string {
	ev := hooks.PostToolUse
	in := map[string]any{
		"tool_name": tc.Name, "tool_input": argsAny(args), "tool_use_id": tc.ID,
		"duration_ms": dur.Milliseconds(),
	}
	if errText != "" {
		ev = hooks.PostToolUseFailure
		in["error"] = errText
		in["is_interrupt"] = ctx.Err() != nil
		in["tool_response"] = map[string]any{"output": output, "error": errText}
	} else {
		in["tool_response"] = map[string]any{"output": output}
	}
	post := a.runHook(ctx, ev, tc.Name, in)
	if post.UpdatedToolOutput != nil {
		logger.Info("Tool %s output replaced by a hook (%d -> %d bytes)", tc.Name, len(content), len(*post.UpdatedToolOutput))
		content = *post.UpdatedToolOutput
	}
	if post.Decision == "block" {
		notes = append(notes, "Hook feedback: "+post.Reason)
	}
	notes = append(notes, hookNotes(post)...)
	return withNotes(content, notes)
}

// postToolBatch runs PostToolBatch once every tool call of a model response
// has a result. Its context is added to the last tool result; a block (or
// continue: false) stops the turn before the next model call.
func (a *Agent) postToolBatch(ctx context.Context, calls []map[string]any) {
	if len(calls) == 0 || a.halt != "" || !a.hookSet().Has(hooks.PostToolBatch) {
		return
	}
	r := a.runHook(ctx, hooks.PostToolBatch, "", map[string]any{"tool_calls": calls})
	if r.Decision == "block" && a.halt == "" {
		a.halt = r.Reason
	}
	if notes := hookNotes(r); len(notes) > 0 {
		for i := len(a.messages) - 1; i >= 0; i-- {
			if a.messages[i].Role == llm.RoleToolResult {
				a.messages[i].Content = withNotes(a.messages[i].Content, notes)
				a.saveCheckpoint()
				break
			}
		}
	}
}

// stopHook runs Stop (SubagentStop for a sub-agent) when the agent is about
// to finish. When a hook blocks the stop or adds context, a message with it
// is added and true is returned: the agent keeps going.
func (a *Agent) stopHook(ctx context.Context, last string) (bool, string) {
	ev := hooks.Stop
	if a.agentType != "" {
		ev = hooks.SubagentStop
	}
	in := map[string]any{
		"last_assistant_message": last, "stop_hook_active": a.stopBlocks > 0,
		"session_crons": []any{},
	}
	var tasks []map[string]any
	if a.backgroundTasks != nil {
		tasks = a.backgroundTasks()
	}
	if tasks == nil {
		tasks = []map[string]any{}
	}
	in["background_tasks"] = tasks
	r := a.runHook(ctx, ev, a.agentType, in)
	if r.StopAll {
		// The turn is ending anyway: just show why.
		if a.halt != "" {
			a.emit(StepResult{Type: "error", Content: "Stopped by a hook: " + a.halt})
		}
		a.halt = ""
	}
	var notes []string
	if r.Decision == "block" {
		notes = append(notes, r.Reason)
	}
	notes = append(notes, r.Context...)
	if r.StopAll || len(notes) == 0 || a.stopBlocks >= maxStopContinuations {
		if len(notes) > 0 {
			logger.Info("Stop hook ignored: %d continuations in a row", a.stopBlocks)
		}
		a.stopBlocks = 0
		return false, ""
	}
	a.stopBlocks++
	note := strings.Join(notes, "\n")
	a.messages = append(a.messages, llm.Message{Role: llm.RoleUser, Content: "[A Stop hook asks you to continue before finishing]\n" + note})
	return true, note
}

// stopFailure runs StopFailure when the turn ends with a model/API error.
// Its output and exit code are ignored.
func (a *Agent) stopFailure(ctx context.Context, err error) {
	if ctx.Err() != nil || !a.hookSet().Has(hooks.StopFailure) {
		return
	}
	kind := ClassifyAPIError(err)
	a.runHook(context.WithoutCancel(ctx), hooks.StopFailure, kind, map[string]any{
		"error": kind, "error_details": err.Error(), "last_assistant_message": "API Error: " + err.Error(),
	})
	a.halt = ""
}

// ClassifyAPIError maps a provider error to a StopFailure error type.
func ClassifyAPIError(err error) string {
	s := strings.ToLower(err.Error())
	has := func(subs ...string) bool {
		for _, x := range subs {
			if strings.Contains(s, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("rate limit", "rate_limit", "429", "too many requests"):
		return "rate_limit"
	case has("overloaded", "529"):
		return "overloaded"
	case has("billing", "credit balance", "insufficient_quota", "payment"):
		return "billing_error"
	case has("401", "403", "unauthorized", "authentication", "invalid api key", "invalid x-api-key", "not logged in"):
		return "authentication_failed"
	case has("model not found", "model_not_found", "unknown model", "404"):
		return "model_not_found"
	case has("max_tokens", "max output tokens", "maximum output"):
		return "max_output_tokens"
	case has("400", "invalid_request", "invalid request", "bad request"):
		return "invalid_request"
	case has("500", "502", "503", "504", "server error", "internal error", "bad gateway", "unavailable"):
		return "server_error"
	}
	return "unknown"
}

// SkillMarker separates what the user wrote from the instructions of a skill
// the user invoked with /name (only the model needs to read them).
const SkillMarker = "\n\n[Skill invoked by the user]\n"

// WithSkill appends a rendered skill to a user message.
func WithSkill(text, rendered string) string {
	return text + SkillMarker + rendered
}
