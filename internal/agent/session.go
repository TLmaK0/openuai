package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"openuai/internal/llm"
	"openuai/internal/logger"
)

// SessionState represents the persisted agent state
type SessionState struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Model     string        `json:"model"`
	Provider  string        `json:"provider"`
	Messages  []llm.Message `json:"messages"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// SessionInfo is a lightweight summary for listing sessions (without messages).
type SessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Messages  int    `json:"messages"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// SaveSession persists the agent's conversation state to disk
func (a *Agent) SaveSession(configDir string) error {
	sessDir := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		return err
	}

	state := SessionState{
		ID:        "current",
		Title:     sessionTitle(a.messages),
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
	return a.loadSessionFrom(path)
}

// LoadSessionByID restores a specific session by ID
func (a *Agent) LoadSessionByID(configDir, id string) error {
	path := filepath.Join(configDir, "sessions", id+".json")
	return a.loadSessionFrom(path)
}

func (a *Agent) loadSessionFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
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

// ArchiveSession saves the current session to history and removes current.json.
// Returns the archive ID or empty string if there was nothing to archive.
func ArchiveSession(configDir string) string {
	sessDir := filepath.Join(configDir, "sessions")
	currentPath := filepath.Join(sessDir, "current.json")

	data, err := os.ReadFile(currentPath)
	if err != nil {
		return ""
	}

	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}

	// Don't archive empty sessions
	if len(state.Messages) == 0 {
		os.Remove(currentPath)
		return ""
	}

	// Generate archive ID from timestamp
	id := fmt.Sprintf("sess_%d", time.Now().UnixMilli())
	state.ID = id
	if state.Title == "" {
		state.Title = sessionTitle(state.Messages)
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = state.UpdatedAt
	}

	archiveData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return ""
	}

	archivePath := filepath.Join(sessDir, id+".json")
	if err := os.WriteFile(archivePath, archiveData, 0o600); err != nil {
		return ""
	}

	os.Remove(currentPath)
	logger.Info("Session archived: %s (%d messages) -> %s", state.Title, len(state.Messages), id)
	return id
}

// ClearSession archives the current session and removes current.json
func ClearSession(configDir string) error {
	ArchiveSession(configDir)
	return nil
}

// ListSessions returns metadata about all archived sessions, newest first.
func ListSessions(configDir string) []SessionInfo {
	sessDir := filepath.Join(configDir, "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return nil
	}

	var sessions []SessionInfo
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "sess_") || !strings.HasSuffix(name, ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(sessDir, name))
		if err != nil {
			continue
		}

		var state SessionState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}

		sessions = append(sessions, SessionInfo{
			ID:        state.ID,
			Title:     state.Title,
			Model:     state.Model,
			Provider:  state.Provider,
			Messages:  len(state.Messages),
			CreatedAt: state.CreatedAt.Format(time.RFC3339),
			UpdatedAt: state.UpdatedAt.Format(time.RFC3339),
		})
	}

	// Sort newest first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})

	return sessions
}

// DeleteSession removes an archived session by ID.
func DeleteSession(configDir, id string) error {
	if id == "current" {
		return fmt.Errorf("cannot delete current session")
	}
	path := filepath.Join(configDir, "sessions", id+".json")
	return os.Remove(path)
}

// Messages returns the current conversation messages (for UI history restore)
func (a *Agent) Messages() []llm.Message {
	return a.messages
}

// sessionTitle extracts a title from the first user message.
func sessionTitle(messages []llm.Message) string {
	for _, m := range messages {
		if m.Role == llm.RoleUser && m.Content != "" {
			title := m.Content
			if len(title) > 60 {
				title = title[:60] + "..."
			}
			return title
		}
	}
	return "Untitled"
}
