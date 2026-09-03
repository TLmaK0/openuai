package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// store is the plugin's own credential file. A plugin keeps its own
// credentials — that is what lets it be a separate program — so the optional
// API key lives here rather than in the core's configuration.
type store struct {
	Secret string `json:"api_key,omitempty"`

	path string
}

func storePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		// Nowhere to persist is not fatal: the ordinary path needs no
		// credential of ours at all.
		return ""
	}
	return filepath.Join(dir, "openuai", "claude-headless.json")
}

func loadStore() *store {
	s := &store{path: storePath()}
	if s.path == "" {
		return s
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	json.Unmarshal(data, s)
	return s
}

func (s *store) save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
