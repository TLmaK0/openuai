package tools

import (
	"context"
	"fmt"
	"strings"

	"openuai/internal/memory"
)

// SaveMemory saves a memory file with optional type/tags/contact metadata.
type SaveMemory struct {
	Store *memory.Store
}

func (t SaveMemory) Definition() Definition {
	return Definition{
		Name:        "save_memory",
		Description: "Save a named memory that persists across sessions. Updates the MEMORY.md index automatically. Use type to categorize: user_profile (preferences, role), project (decisions, context), contact (per-person notes), feedback (corrections, guidance), general (anything else).",
		Parameters: []Parameter{
			{Name: "name", Type: "string", Description: "Short identifier (e.g. 'user_profile', 'project_openuai', 'contact_hugo')", Required: true},
			{Name: "description", Type: "string", Description: "One-line summary shown in the memory index", Required: true},
			{Name: "content", Type: "string", Description: "Full content to remember. Be concise and structured.", Required: true},
			{Name: "type", Type: "string", Description: "Memory type: user_profile, project, contact, feedback, general (default: general)"},
			{Name: "tags", Type: "string", Description: "Comma-separated tags for categorization (e.g. 'preferences,voice')"},
			{Name: "contact", Type: "string", Description: "Contact identifier, for type=contact (e.g. 'hugo@email.com', '+34612345678')"},
		},
	}
}

func (t SaveMemory) Execute(_ context.Context, args map[string]string) Result {
	name, description, content := args["name"], args["description"], args["content"]
	if name == "" || content == "" {
		return Result{Error: "name and content are required"}
	}

	meta := memory.MemoryMeta{
		Type:        args["type"],
		Tags:        args["tags"],
		Contact:     args["contact"],
		Description: description,
	}
	if meta.Type == "" {
		meta.Type = memory.TypeGeneral
	}

	if err := t.Store.SaveWithMeta(name, meta, content); err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: "Memory '" + name + "' saved [" + meta.Type + "]."}
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

// ListMemories returns all stored memories with their type and description.
type ListMemories struct {
	Store *memory.Store
}

func (t ListMemories) Definition() Definition {
	return Definition{
		Name:        "list_memories",
		Description: "List all stored memories with their type and description. Optionally filter by type (user_profile, project, contact, feedback, general).",
		Parameters: []Parameter{
			{Name: "type", Type: "string", Description: "Filter by memory type (optional). Leave empty to list all."},
		},
	}
}

func (t ListMemories) Execute(_ context.Context, args map[string]string) Result {
	entries := t.Store.List(args["type"])
	if len(entries) == 0 {
		return Result{Output: "No memories found."}
	}

	var b strings.Builder
	for _, e := range entries {
		line := fmt.Sprintf("- %s [%s]", e.Name, e.Type)
		if e.Contact != "" {
			line += " (contact: " + e.Contact + ")"
		}
		if e.Tags != "" {
			line += " tags:" + e.Tags
		}
		line += " — " + e.Description
		b.WriteString(line + "\n")
	}
	return Result{Output: b.String()}
}

// SearchMemory searches across all memory content for a keyword or phrase.
type SearchMemory struct {
	Store *memory.Store
}

func (t SearchMemory) Definition() Definition {
	return Definition{
		Name:        "search_memory",
		Description: "Search across all memory files for a keyword or phrase. Returns matching memories with a snippet around the match.",
		Parameters: []Parameter{
			{Name: "query", Type: "string", Description: "Text to search for (case-insensitive)", Required: true},
		},
	}
}

func (t SearchMemory) Execute(_ context.Context, args map[string]string) Result {
	query := args["query"]
	if query == "" {
		return Result{Error: "query is required"}
	}

	results := t.Store.Search(query)
	if len(results) == 0 {
		return Result{Output: "No memories matching '" + query + "'."}
	}

	var b strings.Builder
	for _, r := range results {
		b.WriteString(fmt.Sprintf("- %s [%s]: %s\n", r.Name, r.Type, r.Snippet))
	}
	return Result{Output: b.String()}
}
