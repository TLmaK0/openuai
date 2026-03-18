package memory

import (
	"os"
	"path/filepath"
	"strings"
)

// Store is a simple persistent key-value memory backed by markdown files.
// Each memory is a named .md file in the memory directory.
type Store struct {
	dir string
}

func New(dir string) *Store {
	os.MkdirAll(dir, 0o700)
	return &Store{dir: dir}
}

func (s *Store) Save(name, content string) error {
	return os.WriteFile(filepath.Join(s.dir, sanitize(name)+".md"), []byte(content), 0o600)
}

func (s *Store) Load(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, sanitize(name)+".md"))
	return string(data), err
}

func (s *Store) Delete(name string) error {
	return os.Remove(filepath.Join(s.dir, sanitize(name)+".md"))
}

func (s *Store) List() []string {
	entries, _ := os.ReadDir(s.dir)
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	return names
}

// LoadAll returns all memories concatenated, for injection into the system prompt.
func (s *Store) LoadAll() string {
	names := s.List()
	if len(names) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, name := range names {
		content, err := s.Load(name)
		if err != nil || strings.TrimSpace(content) == "" {
			continue
		}
		sb.WriteString("### " + name + "\n")
		sb.WriteString(strings.TrimSpace(content))
		sb.WriteString("\n\n")
	}
	return sb.String()
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
