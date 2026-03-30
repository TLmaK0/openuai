package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return New(dir)
}

func TestSaveAndLoad(t *testing.T) {
	s := tempStore(t)

	if err := s.Save("test_mem", "a test memory", "hello world"); err != nil {
		t.Fatal(err)
	}

	content, err := s.Load("test_mem")
	if err != nil {
		t.Fatal(err)
	}
	if content != "hello world" {
		t.Fatalf("expected 'hello world', got %q", content)
	}
}

func TestSaveWithMetaAndLoadWithMeta(t *testing.T) {
	s := tempStore(t)

	meta := MemoryMeta{
		Type:        TypeUserProfile,
		Description: "user preferences",
		Tags:        "prefs,voice",
		Contact:     "",
	}
	if err := s.SaveWithMeta("user_prefs", meta, "Prefers dark mode"); err != nil {
		t.Fatal(err)
	}

	gotMeta, body, err := s.LoadWithMeta("user_prefs")
	if err != nil {
		t.Fatal(err)
	}
	if body != "Prefers dark mode" {
		t.Fatalf("body = %q", body)
	}
	if gotMeta.Type != TypeUserProfile {
		t.Fatalf("type = %q", gotMeta.Type)
	}
	if gotMeta.Tags != "prefs,voice" {
		t.Fatalf("tags = %q", gotMeta.Tags)
	}
	if gotMeta.Created == "" {
		t.Fatal("created should be set")
	}
	if gotMeta.Updated == "" {
		t.Fatal("updated should be set")
	}
}

func TestSavePreservesCreatedDate(t *testing.T) {
	s := tempStore(t)

	meta := MemoryMeta{Type: TypeGeneral, Description: "test"}
	if err := s.SaveWithMeta("preserve", meta, "v1"); err != nil {
		t.Fatal(err)
	}

	m1, _, _ := s.LoadWithMeta("preserve")
	created := m1.Created

	// Save again — created should be preserved
	meta.Description = "updated"
	if err := s.SaveWithMeta("preserve", meta, "v2"); err != nil {
		t.Fatal(err)
	}

	m2, body, _ := s.LoadWithMeta("preserve")
	if m2.Created != created {
		t.Fatalf("created changed: %q -> %q", created, m2.Created)
	}
	if body != "v2" {
		t.Fatalf("body not updated: %q", body)
	}
}

func TestListAll(t *testing.T) {
	s := tempStore(t)

	s.SaveWithMeta("mem_a", MemoryMeta{Type: TypeProject, Description: "project A"}, "content A")
	s.SaveWithMeta("mem_b", MemoryMeta{Type: TypeContact, Description: "contact B", Contact: "hugo"}, "content B")
	s.SaveWithMeta("mem_c", MemoryMeta{Type: TypeProject, Description: "project C"}, "content C")

	all := s.List("")
	if len(all) != 3 {
		t.Fatalf("expected 3, got %d", len(all))
	}
}

func TestListByType(t *testing.T) {
	s := tempStore(t)

	s.SaveWithMeta("mem_a", MemoryMeta{Type: TypeProject, Description: "project A"}, "content A")
	s.SaveWithMeta("mem_b", MemoryMeta{Type: TypeContact, Description: "contact B"}, "content B")
	s.SaveWithMeta("mem_c", MemoryMeta{Type: TypeProject, Description: "project C"}, "content C")

	projects := s.List(TypeProject)
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	for _, p := range projects {
		if p.Type != TypeProject {
			t.Fatalf("unexpected type: %s", p.Type)
		}
	}

	contacts := s.List(TypeContact)
	if len(contacts) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(contacts))
	}
}

func TestSearch(t *testing.T) {
	s := tempStore(t)

	s.SaveWithMeta("note1", MemoryMeta{Type: TypeGeneral, Description: "first note"}, "The quick brown fox jumps")
	s.SaveWithMeta("note2", MemoryMeta{Type: TypeGeneral, Description: "second note"}, "A lazy dog sleeps")
	s.SaveWithMeta("note3", MemoryMeta{Type: TypeGeneral, Description: "has fox in description"}, "Nothing here")

	results := s.Search("fox")
	if len(results) != 2 { // note1 body + note3 description
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	results = s.Search("lazy dog")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Name != "note2" {
		t.Fatalf("expected note2, got %s", results[0].Name)
	}

	results = s.Search("nonexistent")
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSearchCaseInsensitive(t *testing.T) {
	s := tempStore(t)
	s.SaveWithMeta("ci", MemoryMeta{Type: TypeGeneral, Description: "test"}, "Hello World")

	results := s.Search("hello world")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestDelete(t *testing.T) {
	s := tempStore(t)
	s.Save("to_delete", "will be deleted", "temp content")

	if err := s.Delete("to_delete"); err != nil {
		t.Fatal(err)
	}

	_, err := s.Load("to_delete")
	if err == nil {
		t.Fatal("expected error after delete")
	}

	// Should not appear in list
	all := s.List("")
	for _, e := range all {
		if e.Name == "to_delete" {
			t.Fatal("deleted memory still in list")
		}
	}
}

func TestBackwardCompatNoFrontmatter(t *testing.T) {
	s := tempStore(t)

	// Write a file without frontmatter (old format)
	path := filepath.Join(s.dir, "old_mem.md")
	os.WriteFile(path, []byte("Just plain content without frontmatter"), 0o600)

	// Load should return the content as-is
	content, err := s.Load("old_mem")
	if err != nil {
		t.Fatal(err)
	}
	if content != "Just plain content without frontmatter" {
		t.Fatalf("unexpected content: %q", content)
	}

	// LoadWithMeta should default to general
	meta, body, err := s.LoadWithMeta("old_mem")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Type != TypeGeneral {
		t.Fatalf("expected general type, got %q", meta.Type)
	}
	if body != "Just plain content without frontmatter" {
		t.Fatalf("body mismatch: %q", body)
	}

	// List should include it
	all := s.List("")
	found := false
	for _, e := range all {
		if e.Name == "old_mem" {
			found = true
			if e.Type != TypeGeneral {
				t.Fatalf("expected general, got %s", e.Type)
			}
		}
	}
	if !found {
		t.Fatal("old_mem not found in list")
	}
}

func TestIndexIncludesType(t *testing.T) {
	s := tempStore(t)

	s.SaveWithMeta("typed", MemoryMeta{Type: TypeFeedback, Description: "some feedback"}, "content")

	index := s.LoadIndex()
	if !strings.Contains(index, "[feedback]") {
		t.Fatalf("index should contain [feedback], got: %s", index)
	}
}

func TestFrontmatterRoundtrip(t *testing.T) {
	meta := MemoryMeta{
		Type:        TypeContact,
		Description: "Hugo's preferences",
		Tags:        "work,dev",
		Contact:     "hugo@example.com",
		Created:     "2026-01-01T00:00:00Z",
		Updated:     "2026-03-30T12:00:00Z",
	}

	fm := buildFrontmatter(meta)
	parsed, body := parseFrontmatter(fm + "Body content here")

	if parsed.Type != meta.Type {
		t.Fatalf("type: %q != %q", parsed.Type, meta.Type)
	}
	if parsed.Description != meta.Description {
		t.Fatalf("description mismatch")
	}
	if parsed.Tags != meta.Tags {
		t.Fatalf("tags mismatch")
	}
	if parsed.Contact != meta.Contact {
		t.Fatalf("contact mismatch")
	}
	if parsed.Created != meta.Created {
		t.Fatalf("created mismatch")
	}
	if parsed.Updated != meta.Updated {
		t.Fatalf("updated mismatch")
	}
	if body != "Body content here" {
		t.Fatalf("body: %q", body)
	}
}
