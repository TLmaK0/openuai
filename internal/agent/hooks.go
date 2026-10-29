package agent

import (
	"context"
	"encoding/json"
	"strings"

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

// runHook runs the hooks of event and reports failures to the user (as an
// error step) and in the log. The caller decides what to tell the model.
func (a *Agent) runHook(ctx context.Context, event, target string, in map[string]any) hooks.Result {
	s := a.hookSet()
	if !s.Has(event) {
		return hooks.Result{}
	}
	if in == nil {
		in = map[string]any{}
	}
	in["session_id"] = "openuai"
	mode := a.permMode
	if mode == "" {
		mode = "default"
	}
	in["permission_mode"] = mode
	if a.agentType != "" {
		in["agent_type"] = a.agentType
		in["agent_id"] = a.agentID
	}
	r := s.Run(ctx, event, target, in, a.workDir)
	if r.Ran > 0 {
		logger.Info("Hooks %s(%s): ran=%d decision=%q context=%d errors=%d", event, target, r.Ran, r.Decision, len(r.Context), len(r.Errors))
	}
	for _, e := range r.Errors {
		logger.Error("Hook error: %s", e)
		a.emit(StepResult{Type: "error", Content: "Hook error: " + e})
	}
	for _, m := range r.Messages {
		logger.Info("Hook message: %s", m)
		a.emit(StepResult{Type: "error", Content: "Hook: " + m})
	}
	return r
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

// toolAllowed applies a PreToolUse "allow" (skip the permission prompt,
// except in plan mode) before the normal permission check.
func (a *Agent) toolAllowed(ctx context.Context, pre hooks.Result, name, required, desc string) (bool, string) {
	if pre.Decision == "allow" && a.permMode != PermModePlan {
		return true, ""
	}
	return a.allowTool(ctx, name, required, desc)
}

// postToolHooks runs PostToolUse / PostToolUseFailure and returns the tool
// result with what the hooks want the model to know appended.
func (a *Agent) postToolHooks(ctx context.Context, tc llm.ToolCall, args map[string]string, output, errText, content string, notes []string) string {
	ev := hooks.PostToolUse
	if errText != "" {
		ev = hooks.PostToolUseFailure
	}
	post := a.runHook(ctx, ev, tc.Name, map[string]any{
		"tool_name": tc.Name, "tool_input": argsAny(args), "tool_use_id": tc.ID,
		"tool_response": map[string]any{"output": output, "error": errText},
	})
	if post.Decision == "block" {
		notes = append(notes, "Hook feedback: "+post.Reason)
	}
	notes = append(notes, hookNotes(post)...)
	return withNotes(content, notes)
}

// stopHook runs Stop (SubagentStop for a sub-agent) when the agent is about
// to finish. When a hook blocks the stop or adds context, a message with it
// is added and true is returned: the agent keeps going.
func (a *Agent) stopHook(ctx context.Context, last string) (bool, string) {
	ev := hooks.Stop
	if a.agentType != "" {
		ev = hooks.SubagentStop
	}
	r := a.runHook(ctx, ev, a.agentType, map[string]any{
		"last_assistant_message": last, "stop_hook_active": a.stopBlocks > 0,
	})
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
