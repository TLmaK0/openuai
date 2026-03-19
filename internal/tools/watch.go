package tools

import "context"

// WatchChatFunc is the callback to watch a chat.
type WatchChatFunc func(jid string) string

// UnwatchChatFunc is the callback to stop watching a chat.
type UnwatchChatFunc func(jid string) string

// WatchChat is a tool that adds a JID to the event watch list.
// All messages from the chat are processed, including own messages.
type WatchChat struct {
	Fn WatchChatFunc
}

func (t WatchChat) Definition() Definition {
	return Definition{
		Name:        "watch_chat",
		Description: "Start watching a chat for new messages (works with WhatsApp, Teams, or any MCP message source). All messages from the chat are processed and trigger the agent automatically.",
		Parameters: []Parameter{
			{Name: "jid", Type: "string", Description: "The chat JID to watch. WhatsApp: '34612345678@s.whatsapp.net'. Teams 1:1 chat: '19:xxx@unq.gbl.spaces'. Teams self-chat/personal notes: '48:notes'. Use list_chats to find the correct ID.", Required: true},
		},
	}
}

func (t WatchChat) Execute(_ context.Context, args map[string]string) Result {
	jid := args["jid"]
	if jid == "" {
		return Result{Error: "jid is required"}
	}
	return Result{Output: t.Fn(jid)}
}

// UnwatchChat is a tool that removes a JID from the event watch list.
type UnwatchChat struct {
	Fn UnwatchChatFunc
}

func (t UnwatchChat) Definition() Definition {
	return Definition{
		Name:        "unwatch_chat",
		Description: "Stop watching a chat for new messages (WhatsApp, Teams, or any MCP source).",
		Parameters: []Parameter{
			{Name: "jid", Type: "string", Description: "The chat JID or phone number to stop watching", Required: true},
		},
	}
}

func (t UnwatchChat) Execute(_ context.Context, args map[string]string) Result {
	jid := args["jid"]
	if jid == "" {
		return Result{Error: "jid is required"}
	}
	return Result{Output: t.Fn(jid)}
}
