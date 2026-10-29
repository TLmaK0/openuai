package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"openuai/internal/hooks"
	"openuai/internal/llm"
	"openuai/internal/logger"
)

// DefaultSubAgentCompactTokens is the estimated context size at which a
// sub-agent compacts its own history instead of failing when it fills up.
const DefaultSubAgentCompactTokens = 120000

// CompactNotePrefix starts the summary that replaces compacted history.
const CompactNotePrefix = "[Context compacted: summary of the earlier part of this task]"

// compactKeepTail is how many recent messages are always kept verbatim.
const compactKeepTail = 8

const maxCompactInput = 400000

const compactPrompt = `You are compacting the context of an AI agent whose conversation is too long to continue.
Summarize the conversation below so the agent can carry on with the same work without it:
- restate the original task verbatim;
- what has been done so far and its results;
- key findings with exact file paths, values, commands and errors;
- decisions taken and what remains to do.
Be concise but keep every fact needed to continue. Output only the summary.`

// SetHistory replaces the conversation with a saved one (resuming a
// sub-agent), keeping this agent's own system prompt. Tool calls left without
// a result (e.g. the sub-agent was cancelled mid-tool) are closed.
func (a *Agent) SetHistory(msgs []llm.Message) {
	if len(msgs) > 0 && msgs[0].Role == llm.RoleSystem {
		msgs = msgs[1:]
	}
	a.messages = append([]llm.Message{a.messages[0]}, msgs...)
	a.closeDanglingToolCalls()
}

func estimateTokens(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, tc := range m.ToolCalls {
			n += len(tc.Name)
			for k, v := range tc.Arguments {
				n += len(k) + len(v)
			}
		}
		n += 4000 * len(m.Images)
	}
	return n / 4
}

// compactCut returns where the kept tail starts: at least keep messages from
// the end, on a user or assistant message (never on a tool result, which needs
// its tool call). 0 means there is nothing that can be compacted.
func compactCut(msgs []llm.Message, keep int) int {
	for i := len(msgs) - keep; i >= 2; i-- {
		if r := msgs[i].Role; r == llm.RoleUser || r == llm.RoleAssistant {
			return i
		}
	}
	return 0
}

// maybeCompact summarizes the older part of the conversation when it grows
// past the agent's compaction threshold, so a long run keeps going instead of
// failing on a full context. On any error the history is left as it was.
func (a *Agent) maybeCompact(ctx context.Context) {
	if a.compactAt <= 0 {
		return
	}
	before := estimateTokens(a.messages)
	if before < a.compactAt {
		return
	}
	cut := compactCut(a.messages, compactKeepTail)
	if cut == 0 {
		return
	}
	// PreCompact hooks can block the compaction.
	if pre := a.runHook(ctx, hooks.PreCompact, "auto", map[string]any{"trigger": "auto", "custom_instructions": nil}); pre.Decision == "block" {
		logger.Info("Context compaction blocked by a PreCompact hook: %s", pre.Reason)
		return
	}
	var b strings.Builder
	for _, m := range a.messages[1:cut] {
		if m.Content != "" {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
		}
		for _, tc := range m.ToolCalls {
			keys := make([]string, 0, len(tc.Arguments))
			for k := range tc.Arguments {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s=%q", k, tc.Arguments[k]))
			}
			fmt.Fprintf(&b, "%s: called %s(%s)\n", m.Role, tc.Name, strings.Join(parts, ", "))
		}
	}
	text := b.String()
	if len(text) > maxCompactInput {
		text = text[len(text)-maxCompactInput:]
	}
	resp, err := a.provider.Chat(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: compactPrompt},
		{Role: llm.RoleUser, Content: text},
	}, a.model)
	if err != nil || resp == nil || strings.TrimSpace(resp.Content) == "" {
		if err != nil {
			logger.Error("Context compaction failed, keeping history: %s", err.Error())
		}
		return
	}
	if a.costTracker != nil {
		a.costTracker.Track(resp)
	}
	summaryText := strings.TrimSpace(resp.Content)
	summary := llm.Message{Role: llm.RoleUser, Content: CompactNotePrefix + "\n" + summaryText}
	// The main conversation starts again from the summary: SessionStart
	// hooks with source "compact" can add context to it.
	if a.agentType == "" {
		ss := a.runHook(ctx, hooks.SessionStart, "compact", map[string]any{"source": "compact", "model": a.model})
		if notes := hookNotes(ss); len(notes) > 0 {
			summary.Content = WithHookContext(summary.Content, notes)
		}
	}
	msgs := make([]llm.Message, 0, 2+len(a.messages)-cut)
	msgs = append(msgs, a.messages[0], summary)
	msgs = append(msgs, a.messages[cut:]...)
	a.messages = msgs
	logger.Info("Context compacted: ~%d -> ~%d tokens (%d messages summarized)", before, estimateTokens(a.messages), cut-1)
	a.runHook(ctx, hooks.PostCompact, "auto", map[string]any{"trigger": "auto", "compact_summary": summaryText})
}
