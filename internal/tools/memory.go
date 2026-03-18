package tools

import (
	"context"

	"openuai/internal/memory"
)

// SaveMemory saves a memory file and updates the MEMORY.md index.
type SaveMemory struct {
	Store *memory.Store
}

func (t SaveMemory) Definition() Definition {
	return Definition{
		Name:        "save_memory",
		Description: "Save a named memory that persists across sessions. Updates the MEMORY.md index automatically. Use for user preferences, project context, per-contact notes, or anything worth remembering long-term.",
		Parameters: []Parameter{
			{Name: "name", Type: "string", Description: "Short identifier (e.g. 'user_profile', 'project_openuai', 'contact_hugo')", Required: true},
			{Name: "description", Type: "string", Description: "One-line summary shown in the memory index", Required: true},
			{Name: "content", Type: "string", Description: "Full content to remember. Be concise and structured.", Required: true},
		},
	}
}

func (t SaveMemory) Execute(_ context.Context, args map[string]string) Result {
	name, description, content := args["name"], args["description"], args["content"]
	if name == "" || content == "" {
		return Result{Error: "name and content are required"}
	}
	if err := t.Store.Save(name, description, content); err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: "Memory '" + name + "' saved and indexed."}
}

// ReadMemory loads a specific memory file by name.
type ReadMemory struct {
	Store *memory.Store
}

func (t ReadMemory) Definition() Definition {
	return Definition{
		Name:        "read_memory",
		Description: "Load the full content of a specific memory file. Use when you need details beyond what's in the MEMORY.md index.",
		Parameters: []Parameter{
			{Name: "name", Type: "string", Description: "Name of the memory to read (without .md extension)", Required: true},
		},
	}
}

func (t ReadMemory) Execute(_ context.Context, args map[string]string) Result {
	name := args["name"]
	if name == "" {
		return Result{Error: "name is required"}
	}
	content, err := t.Store.Load(name)
	if err != nil {
		return Result{Error: "Memory not found: " + name}
	}
	return Result{Output: content}
}

// DeleteMemory removes a memory file and its index entry.
type DeleteMemory struct {
	Store *memory.Store
}

func (t DeleteMemory) Definition() Definition {
	return Definition{
		Name:        "delete_memory",
		Description: "Delete a memory file and remove it from the index.",
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
