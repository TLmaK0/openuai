package memory

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const indexFile = "MEMORY.md"
const indexMaxLines = 200

// Memory types (soft enum — the LLM decides which to use).
const (
	TypeUserProfile = "user_profile"
	TypeProject     = "project"
	TypeContact     = "contact"
	TypeFeedback    = "feedback"
	TypeGeneral     = "general"
)

// MemoryMeta holds metadata stored as YAML-like frontmatter in each .md file.
type MemoryMeta struct {
	Type        string // one of the Type* constants
	Tags        string // comma-separated tags
	Contact     string // contact identifier (for type=contact)
	Description string // one-line summary shown in index
	Created     string // RFC3339
	Updated     string // RFC3339
}

// MemoryEntry is returned by List — summary without full content.
type MemoryEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Tags        string `json:"tags,omitempty"`
	Contact     string `json:"contact,omitempty"`
	Updated     string `json:"updated,omitempty"`
}

// SearchResult is returned by Search — name plus a snippet around the match.
type SearchResult struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Snippet string `json:"snippet"`
}

// Store is a file-based memory system.
// MEMORY.md is the index — loaded automatically into every session (up to 200 lines).
// Individual .md files hold the full content of each memory, loaded on demand.
type Store struct {
	dir string
}

func New(dir string) *Store {
	os.MkdirAll(dir, 0o700)
	return &Store{dir: dir}
}

// LoadIndex returns the MEMORY.md index, truncated to 200 lines.
// This is injected into the system prompt at session start.
func (s *Store) LoadIndex() string {
	data, err := os.ReadFile(filepath.Join(s.dir, indexFile))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > indexMaxLines {
		lines = lines[:indexMaxLines]
	}
	return strings.Join(lines, "\n")
}

// Save writes a memory file (backward-compatible — type defaults to "general").
func (s *Store) Save(name, description, content string) error {
	return s.SaveWithMeta(name, MemoryMeta{
		Type:        TypeGeneral,
		Description: description,
	}, content)
}

// SaveWithMeta writes a memory file with typed frontmatter and updates the index.
func (s *Store) SaveWithMeta(name string, meta MemoryMeta, content string) error {
	name = sanitize(name)
	now := time.Now().Format(time.RFC3339)

	// Preserve created date from existing file
	if existing, err := os.ReadFile(filepath.Join(s.dir, name+".md")); err == nil {
		if oldMeta, _ := parseFrontmatter(string(existing)); oldMeta.Created != "" {
			meta.Created = oldMeta.Created
		}
	}
	if meta.Created == "" {
		meta.Created = now
	}
	meta.Updated = now
	if meta.Type == "" {
		meta.Type = TypeGeneral
	}

	fileContent := buildFrontmatter(meta) + content

	if err := os.WriteFile(filepath.Join(s.dir, name+".md"), []byte(fileContent), 0o600); err != nil {
		return err
	}
	return s.updateIndex(name, meta.Type, meta.Description)
}

// Load reads a specific memory file by name (returns body without frontmatter).
func (s *Store) Load(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, sanitize(name)+".md"))
	if err != nil {
		return "", err
	}
	_, body := parseFrontmatter(string(data))
	return body, nil
}

// LoadWithMeta reads a memory file and returns both metadata and body.
func (s *Store) LoadWithMeta(name string) (MemoryMeta, string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, sanitize(name)+".md"))
	if err != nil {
		return MemoryMeta{}, "", err
	}
	meta, body := parseFrontmatter(string(data))
	return meta, body, nil
}

// Delete removes a memory file and its entry from the index.
func (s *Store) Delete(name string) error {
	name = sanitize(name)
	os.Remove(filepath.Join(s.dir, name+".md"))
	return s.removeFromIndex(name)
}

// List returns all memories, optionally filtered by type.
// Pass empty typeFilter to list all.
func (s *Store) List(typeFilter string) []MemoryEntry {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}

	var result []MemoryEntry
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") || name == indexFile {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}

		meta, _ := parseFrontmatter(string(data))
		memName := strings.TrimSuffix(name, ".md")

		if typeFilter != "" && meta.Type != typeFilter {
			continue
		}

		result = append(result, MemoryEntry{
			Name:        memName,
			Type:        meta.Type,
			Description: meta.Description,
			Tags:        meta.Tags,
			Contact:     meta.Contact,
			Updated:     meta.Updated,
		})
	}

	// Sort by updated time descending (newest first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Updated > result[j].Updated
	})

	return result
}

// Search does case-insensitive substring search across all memory bodies.
// Returns matching memories with a snippet around the first match.
func (s *Store) Search(query string) []SearchResult {
	if query == "" {
		return nil
	}
	queryLower := strings.ToLower(query)

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}

	var results []SearchResult
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") || name == indexFile {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}

		meta, body := parseFrontmatter(string(data))
		bodyLower := strings.ToLower(body)

		idx := strings.Index(bodyLower, queryLower)
		if idx == -1 {
			// Also search in description
			if strings.Contains(strings.ToLower(meta.Description), queryLower) {
				results = append(results, SearchResult{
					Name:    strings.TrimSuffix(name, ".md"),
					Type:    meta.Type,
					Snippet: meta.Description,
				})
			}
			continue
		}

		snippet := extractSnippet(body, idx, 120)
		results = append(results, SearchResult{
			Name:    strings.TrimSuffix(name, ".md"),
			Type:    meta.Type,
			Snippet: snippet,
		})
	}

	return results
}

// --- Frontmatter parsing ---

func parseFrontmatter(content string) (MemoryMeta, string) {
	if !strings.HasPrefix(content, "---\n") {
		return MemoryMeta{Type: TypeGeneral}, content
	}

	end := strings.Index(content[4:], "\n---\n")
	if end == -1 {
		return MemoryMeta{Type: TypeGeneral}, content
	}

	fmBlock := content[4 : 4+end]
	body := content[4+end+5:] // skip past closing "---\n"

	meta := MemoryMeta{Type: TypeGeneral}
	for _, line := range strings.Split(fmBlock, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch key {
		case "type":
			meta.Type = val
		case "tags":
			meta.Tags = val
		case "contact":
			meta.Contact = val
		case "description":
			meta.Description = val
		case "created":
			meta.Created = val
		case "updated":
			meta.Updated = val
		}
	}
	return meta, body
}

func buildFrontmatter(meta MemoryMeta) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: " + meta.Type + "\n")
	if meta.Description != "" {
		b.WriteString("description: " + meta.Description + "\n")
	}
	if meta.Tags != "" {
		b.WriteString("tags: " + meta.Tags + "\n")
	}
	if meta.Contact != "" {
		b.WriteString("contact: " + meta.Contact + "\n")
	}
	if meta.Created != "" {
		b.WriteString("created: " + meta.Created + "\n")
	}
	if meta.Updated != "" {
		b.WriteString("updated: " + meta.Updated + "\n")
	}
	b.WriteString("---\n")
	return b.String()
}

// --- Index management ---

func (s *Store) updateIndex(name, memType, description string) error {
	index := s.LoadIndex()
	lines := strings.Split(index, "\n")

	entry := "- [" + name + ".md](" + name + ".md) [" + memType + "] — " + description
	prefix := "- [" + name + ".md]"

	updated := false
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = entry
			updated = true
			break
		}
	}
	if !updated {
		if strings.TrimSpace(index) == "" {
			lines = []string{"# Memory Index", "", entry}
		} else {
			lines = append(lines, entry)
		}
	}

	return os.WriteFile(filepath.Join(s.dir, indexFile), []byte(strings.Join(lines, "\n")), 0o600)
}

func (s *Store) removeFromIndex(name string) error {
	index := s.LoadIndex()
	if index == "" {
		return nil
	}
	prefix := "- [" + name + ".md]"
	var lines []string
	for _, line := range strings.Split(index, "\n") {
		if !strings.HasPrefix(line, prefix) {
			lines = append(lines, line)
		}
	}
	return os.WriteFile(filepath.Join(s.dir, indexFile), []byte(strings.Join(lines, "\n")), 0o600)
}

// --- Helpers ---

func extractSnippet(text string, matchIdx, maxLen int) string {
	start := matchIdx - maxLen/2
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(text) {
		end = len(text)
	}
	snippet := text[start:end]
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(text) {
		snippet = snippet + "..."
	}
	return snippet
}

func sanitize(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
