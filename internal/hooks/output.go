package hooks

import (
	"encoding/json"
	"fmt"
	"strings"
)

// output is the JSON a hook can print (command), return (http) or answer
// (mcp_tool). Fields with the wrong type make it invalid, like Claude Code's
// schema validation.
type output struct {
	Continue         *bool  `json:"continue"`
	StopReason       string `json:"stopReason"`
	SuppressOutput   bool   `json:"suppressOutput"`
	SystemMessage    string `json:"systemMessage"`
	TerminalSequence string `json:"terminalSequence"` // accepted, ignored: OpenUAI has no terminal
	Decision         string `json:"decision"`
	Reason           string `json:"reason"`

	HookSpecificOutput struct {
		HookEventName            string         `json:"hookEventName"`
		PermissionDecision       string         `json:"permissionDecision"`
		PermissionDecisionReason string         `json:"permissionDecisionReason"`
		UpdatedInput             map[string]any `json:"updatedInput"`
		AdditionalContext        string         `json:"additionalContext"`
		Decision                 *struct {
			Behavior           string         `json:"behavior"`
			UpdatedInput       map[string]any `json:"updatedInput"`
			UpdatedPermissions []any          `json:"updatedPermissions"`
			Message            string         `json:"message"`
			Interrupt          bool           `json:"interrupt"`
		} `json:"decision"`
		Retry                  bool            `json:"retry"`
		UpdatedToolOutput      json.RawMessage `json:"updatedToolOutput"`
		UpdatedMCPToolOutput   json.RawMessage `json:"updatedMCPToolOutput"`
		ClassifierContext      string          `json:"classifierContext"` // no classifier in OpenUAI
		InitialUserMessage     string          `json:"initialUserMessage"`
		SessionTitle           string          `json:"sessionTitle"`
		WatchPaths             []string        `json:"watchPaths"`
		ReloadSkills           bool            `json:"reloadSkills"` // skills are always read fresh
		SuppressOriginalPrompt bool            `json:"suppressOriginalPrompt"`
		DisplayContent         *string         `json:"displayContent"`
		WorktreePath           string          `json:"worktreePath"`
	} `json:"hookSpecificOutput"`
}

var outputFields = map[string]bool{
	"continue": true, "stopReason": true, "suppressOutput": true, "systemMessage": true,
	"terminalSequence": true, "decision": true, "reason": true, "hookSpecificOutput": true,
}

// parseStdout reads hook output by Claude Code's rules: one JSON object
// (alone, surrounded only by whitespace) is JSON output; text that does not
// start with { and end with } is plain text; anything else is a parse
// failure (failure is then set).
func parseStdout(out string) (parsed *output, plain, failure string) {
	t := strings.TrimSpace(out)
	if t == "" {
		return nil, "", ""
	}
	if !strings.HasPrefix(t, "{") || !strings.HasSuffix(t, "}") {
		return nil, t, ""
	}
	var o output
	err := json.Unmarshal([]byte(t), &o)
	if err == nil {
		return &o, "", ""
	}
	// Several lines that are each valid JSON: plain text, unless one of them
	// is an output object (then it is a failure: print that object alone).
	lines := strings.Split(t, "\n")
	if len(lines) > 1 {
		allJSON, setsField := true, false
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			var v any
			if json.Unmarshal([]byte(l), &v) != nil {
				allJSON = false
				break
			}
			if m, ok := v.(map[string]any); ok {
				for k := range m {
					if outputFields[k] {
						setsField = true
					}
				}
			}
		}
		if allJSON && !setsField {
			return nil, t, ""
		}
		if allJSON {
			return nil, "", "printed several JSON values: print the output object alone"
		}
	}
	return nil, "", fmt.Sprintf("printed invalid JSON: %v", err)
}

// parseAsyncOutput finds the JSON response of a background hook: alone on
// stdout, or on one line of its own.
func parseAsyncOutput(out string) *output {
	if o, _, _ := parseStdout(out); o != nil {
		return o
	}
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "{") && strings.HasSuffix(l, "}") {
			var o output
			if json.Unmarshal([]byte(l), &o) == nil {
				return &o
			}
		}
	}
	return nil
}

// Events where additionalContext reaches the model.
var contextEvents = map[string]bool{
	SessionStart: true, SubagentStart: true, UserPromptSubmit: true, UserPromptExpansion: true,
	PreToolUse: true, PostToolUse: true, PostToolUseFailure: true, PostToolBatch: true,
	Stop: true, SubagentStop: true, PostModelSwitch: true,
}

// Events whose JSON output is discarded entirely.
var discardOutput = map[string]bool{
	StopFailure: true, InstructionsLoaded: true, SessionEnd: true, WorktreeRemove: true, Notification: true,
}

// Events that discard systemMessage and continue.
var discardMessage = map[string]bool{
	ConfigChange: true, PreCompact: true, PostCompact: true, MessageDisplay: true, WorktreeCreate: true,
}

// Events whose top-level decision "block" is honored.
var topBlock = map[string]bool{
	UserPromptSubmit: true, UserPromptExpansion: true, PostToolUse: true, PostToolBatch: true,
	Stop: true, SubagentStop: true, ConfigChange: true, PreCompact: true,
}

// jsonBlockReason is the reason of a blocking decision in o, if any.
func jsonBlockReason(event string, o *output) string {
	h := o.HookSpecificOutput
	if (event == PreToolUse || event == PreModelSwitch) && h.PermissionDecision == "deny" && h.PermissionDecisionReason != "" {
		return h.PermissionDecisionReason
	}
	if strings.EqualFold(o.Decision, "block") && o.Reason != "" {
		return o.Reason
	}
	return ""
}

// applyOutput applies the JSON output o of one hook on event. When blocked
// (exit code 2) the fields are read but cannot override the block.
func applyOutput(r *Result, event string, o *output, blocked bool) {
	if discardOutput[event] {
		return
	}
	if !discardMessage[event] {
		if o.Continue != nil && !*o.Continue && event != CwdChanged && event != FileChanged {
			r.StopAll = true
			r.StopReason = capText(o.StopReason)
		}
		if o.SystemMessage != "" {
			r.Messages = append(r.Messages, capText(o.SystemMessage))
		}
	}
	h := o.HookSpecificOutput
	if c := h.AdditionalContext; c != "" && contextEvents[event] && !(event == PreToolUse && h.PermissionDecision == "defer") {
		r.Context = append(r.Context, escapeReminder(capText(c)))
	}
	switch event {
	case SessionStart:
		if h.InitialUserMessage != "" {
			r.InitialUserMessage = capText(h.InitialUserMessage)
		}
		r.SessionTitle = h.SessionTitle
		if h.WatchPaths != nil {
			r.WatchPaths, r.WatchPathsSet = h.WatchPaths, true
		}
	case UserPromptSubmit:
		r.SessionTitle = h.SessionTitle
		r.SuppressPrompt = h.SuppressOriginalPrompt
	case CwdChanged, FileChanged:
		if h.WatchPaths != nil {
			r.WatchPaths, r.WatchPathsSet = h.WatchPaths, true
		}
	}
	if blocked {
		return
	}
	switch event {
	case PreToolUse, PreModelSwitch:
		d := h.PermissionDecision
		reason := h.PermissionDecisionReason
		switch strings.ToLower(o.Decision) { // deprecated top-level fields
		case "approve":
			if d == "" {
				d = "allow"
			}
		case "block":
			if d == "" {
				d = "deny"
			}
		}
		if reason == "" {
			reason = o.Reason
		}
		if d == "defer" {
			if event == PreModelSwitch {
				d = ""
			} else {
				// defer pauses a non-interactive run so its caller can answer;
				// OpenUAI always has the user at hand, so it asks them.
				d = "ask"
			}
		}
		switch d {
		case "allow", "ask", "deny":
			r.Decision, r.Reason = d, capText(reason)
		}
		if h.UpdatedInput != nil && event == PreToolUse {
			r.UpdatedInput = h.UpdatedInput
		}
	case PermissionRequest:
		if d := h.Decision; d != nil {
			switch d.Behavior {
			case "allow":
				r.Decision = "allow"
				r.UpdatedInput = d.UpdatedInput
			case "deny":
				r.Decision, r.Reason = "deny", capText(d.Message)
				if d.Interrupt {
					r.StopAll = true
					if r.StopReason == "" {
						r.StopReason = capText(d.Message)
					}
				}
			}
		}
	case PermissionDenied:
		r.Retry = h.Retry
	case MessageDisplay:
		r.DisplayContent = h.DisplayContent
	case WorktreeCreate:
		r.WorktreePath = strings.TrimSpace(h.WorktreePath)
	}
	if event == PostToolUse {
		raw := h.UpdatedToolOutput
		if len(raw) == 0 || string(raw) == "null" {
			raw = h.UpdatedMCPToolOutput
		}
		if len(raw) > 0 && string(raw) != "null" {
			text := toolOutputText(raw)
			r.UpdatedToolOutput = &text
		}
	}
	if topBlock[event] && strings.EqualFold(o.Decision, "block") {
		r.Decision, r.Reason = "block", capText(o.Reason)
	}
}

// toolOutputText turns an updatedToolOutput value into the text the model
// sees: OpenUAI tools return text, so a string is used as is, an object with
// an "output" (or "stdout") string gives that string, and anything else is
// passed as JSON.
func toolOutputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		for _, k := range []string{"output", "stdout", "content", "text"} {
			if v, ok := m[k].(string); ok {
				return v
			}
		}
	}
	return string(raw)
}
