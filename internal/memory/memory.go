package memory

import (
	"os"
	"path/filepath"
	"strings"
)

const indexFile = "MEMORY.md"
const indexMaxLines = 200

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

// Save writes a memory file and updates the MEMORY.md index entry for it.
func (s *Store) Save(name, description, content string) error {
	name = sanitize(name)
	if err := os.WriteFile(filepath.Join(s.dir, name+".md"), []byte(content), 0o600); err != nil {
		return err
	}
	return s.updateIndex(name, description)
}

// Load reads a specific memory file by name.
func (s *Store) Load(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, sanitize(name)+".md"))
	return string(data), err
}

// Delete removes a memory file and its entry from the index.
func (s *Store) Delete(name string) error {
	name = sanitize(name)
	os.Remove(filepath.Join(s.dir, name+".md"))
	return s.removeFromIndex(name)
}

// updateIndex adds or updates the line for `name` in MEMORY.md.
func (s *Store) updateIndex(name, description string) error {
	index := s.LoadIndex()
	lines := strings.Split(index, "\n")

	entry := "- [" + name + ".md](" + name + ".md) — " + description
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
		// Add header if index is empty
		if strings.TrimSpace(index) == "" {
			lines = []string{"# Memory Index", "", entry}
		} else {
			lines = append(lines, entry)
		}
	}

	return os.WriteFile(filepath.Join(s.dir, indexFile), []byte(strings.Join(lines, "\n")), 0o600)
}

// removeFromIndex removes the entry for `name` from MEMORY.md.
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

// sanitize restricts memory names to safe filename characters.
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
