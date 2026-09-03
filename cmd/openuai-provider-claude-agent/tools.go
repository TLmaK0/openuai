package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"openuai/internal/llm"
)

// toolSchema constrains the answer when openuai offers tools. Arguments are a
// string map because that is what llm.ToolCall carries: openuai's tool
// parameters are flat, so nothing is lost by saying so here rather than
// accepting a shape the core cannot hold.
const toolSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "reply": {
      "type": "string",
      "description": "Text for the user. Empty when calling tools instead."
    },
    "tool_calls": {
      "type": "array",
      "description": "Tools to run. Empty when replying with text instead.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "name": {"type": "string"},
          "arguments": {"type": "object", "additionalProperties": {"type": "string"}}
        },
        "required": ["name", "arguments"]
      }
    }
  },
  "required": ["reply", "tool_calls"]
}`

// toolInstruction tells the model how the two halves of the schema are meant
// to be used. The schema can require both fields but cannot say that filling
// one means leaving the other empty.
const toolInstruction = "Either call tools or reply with text, never both: put tool calls in tool_calls " +
	"with reply empty, or put your answer in reply with tool_calls empty. Only call tools from the list " +
	"given to you, and pass every required argument."

// answer is the structured reply.
type answer struct {
	Reply     string `json:"reply"`
	ToolCalls []struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	} `json:"tool_calls"`
}

func (a answer) toolCalls() []llm.ToolCall {
	if len(a.ToolCalls) == 0 {
		return nil
	}
	calls := make([]llm.ToolCall, 0, len(a.ToolCalls))
	for i, c := range a.ToolCalls {
		if c.Name == "" {
			continue
		}
		calls = append(calls, llm.ToolCall{
			// The core matches a result back to its call by this id, and the
			// headless agent issues none of its own, so one is minted here.
			ID:        fmt.Sprintf("call_%d", i+1),
			Name:      c.Name,
			Arguments: c.Arguments,
		})
	}
	return calls
}

// parseAnswer reads the structured output, falling back to the plain text when
// the run produced no structured field — which happens when the model answers
// without calling anything and the schema was not applied.
func parseAnswer(out *outcome) (answer, error) {
	if len(out.StructuredOutput) == 0 {
		return answer{Reply: out.Text}, nil
	}
	var a answer
	if err := json.Unmarshal(out.StructuredOutput, &a); err != nil {
		return answer{}, fmt.Errorf("the headless agent returned an answer this plugin cannot read: %w", err)
	}
	return a, nil
}

// renderPrompt flattens the conversation into the single prompt a headless run
// takes. The roles are labelled rather than dropped, because the loop relies
// on the model seeing which tool result belongs to which call.
func renderPrompt(messages []llm.Message, tools []llm.ToolDefinition) string {
	var b strings.Builder

	if len(tools) > 0 {
		b.WriteString("Tools available to the agent you answer to:\n\n")
		for _, t := range tools {
			fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Description)
			for _, p := range t.Parameters {
				required := "optional"
				if p.Required {
					required = "required"
				}
				fmt.Fprintf(&b, "    %s (%s, %s): %s\n", p.Name, p.Type, required, p.Description)
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("Conversation:\n\n")
	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem:
			fmt.Fprintf(&b, "[instructions]\n%s\n\n", m.Content)
		case llm.RoleUser:
			fmt.Fprintf(&b, "[user]\n%s\n\n", m.Content)
		case llm.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				for _, c := range m.ToolCalls {
					args, _ := json.Marshal(c.Arguments)
					fmt.Fprintf(&b, "[assistant called %s id=%s]\n%s\n\n", c.Name, c.ID, args)
				}
			}
			if m.Content != "" {
				fmt.Fprintf(&b, "[assistant]\n%s\n\n", m.Content)
			}
		case llm.RoleToolResult:
			fmt.Fprintf(&b, "[result of call id=%s]\n%s\n\n", m.ToolCallID, m.Content)
		default:
			fmt.Fprintf(&b, "[%s]\n%s\n\n", m.Role, m.Content)
		}
		if len(m.Images) > 0 {
			// Images cannot cross this boundary: a headless run takes a
			// prompt, not image inputs. Saying so is better than dropping
			// them silently, because computer-use turns depend on them.
			fmt.Fprintf(&b, "[%d image(s) omitted: this provider cannot pass images]\n\n", len(m.Images))
		}
	}
	return b.String()
}
