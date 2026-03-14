package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"openuai/internal/llm"
	"openuai/internal/logger"
)

// SessionState represents the persisted agent state
type SessionState struct {
	ID        string        `json:"id"`
	Model     string        `json:"model"`
	Provider  string        `json:"provider"`
	Messages  []llm.Message `json:"messages"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// SaveSession persists the agent's conversation state to disk
func (a *Agent) SaveSession(configDir string) error {
	sessDir := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		return err
	}

	state := SessionState{
		ID:        "current",
		Model:     a.model,
		Provider:  a.provider.Name(),
		Messages:  a.messages,
		UpdatedAt: time.Now(),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(sessDir, "current.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}

	logger.Info("Session saved: %d messages to %s", len(a.messages), path)
	return nil
}

// LoadSession restores the agent's conversation state from disk
func (a *Agent) LoadSession(configDir string) error {
	path := filepath.Join(configDir, "sessions", "current.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no session to restore
		}
		return err
	}

	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	// Only restore if same provider and model
	if state.Provider != a.provider.Name() || state.Model != a.model {
		logger.Info("Session provider/model mismatch, starting fresh")
		return nil
	}

	a.messages = state.Messages
	logger.Info("Session restored: %d messages from %s", len(a.messages), path)
	return nil
}

// ClearSession removes the persisted session
func ClearSession(configDir string) error {
	path := filepath.Join(configDir, "sessions", "current.json")
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Messages returns the current conversation messages (for UI history restore)
func (a *Agent) Messages() []llm.Message {
	return a.messages
}
