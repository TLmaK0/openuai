package tools

import (
	"context"
	"strings"

	"openuai/internal/memory"
)

// SaveMemory saves or updates a named memory.
type SaveMemory struct {
	Store *memory.Store
}

func (t SaveMemory) Definition() Definition {
	return Definition{
		Name:        "save_memory",
		Description: "Save or update a named memory that persists across sessions. Use this to remember important information about the user, their preferences, ongoing projects, or anything worth retaining long-term.",
		Parameters: []Parameter{
			{Name: "name", Type: "string", Description: "Short identifier for the memory (e.g. 'user_profile', 'project_context', 'contact_hugo')", Required: true},
			{Name: "content", Type: "string", Description: "The content to remember. Be concise and structured.", Required: true},
		},
	}
}

func (t SaveMemory) Execute(_ context.Context, args map[string]string) Result {
	name := args["name"]
	content := args["content"]
	if name == "" || content == "" {
		return Result{Error: "name and content are required"}
	}
	if err := t.Store.Save(name, content); err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: "Memory '" + name + "' saved."}
}

// ListMemories lists all saved memories and their content.
type ListMemories struct {
	Store *memory.Store
}

func (t ListMemories) Definition() Definition {
	return Definition{
		Name:        "list_memories",
		Description: "List all saved memories with their content.",
		Parameters:  []Parameter{},
	}
}

func (t ListMemories) Execute(_ context.Context, _ map[string]string) Result {
	names := t.Store.List()
	if len(names) == 0 {
		return Result{Output: "No memories saved yet."}
	}
	var sb strings.Builder
	for _, name := range names {
		content, err := t.Store.Load(name)
		if err != nil {
			continue
		}
		sb.WriteString("## " + name + "\n")
		sb.WriteString(strings.TrimSpace(content))
		sb.WriteString("\n\n")
	}
	return Result{Output: sb.String()}
}

// DeleteMemory deletes a named memory.
type DeleteMemory struct {
	Store *memory.Store
}

func (t DeleteMemory) Definition() Definition {
	return Definition{
		Name:        "delete_memory",
		Description: "Delete a named memory.",
		Parameters: []Parameter{
			{Name: "name", Type: "string", Description: "Name of the memory to delete", Required: true},
		},
	}
}

func (t DeleteMemory) Execute(_ context.Context, args map[string]string) Result {
	name := args["name"]
	if name == "" {
		return Result{Error: "name is required"}
	}
	if err := t.Store.Delete(name); err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: "Memory '" + name + "' deleted."}
}
