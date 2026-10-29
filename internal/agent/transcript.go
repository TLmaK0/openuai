package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"openuai/internal/llm"
)

// TranscriptRetention is how long a sub-agent's saved work is kept after its
// last run (same default as Claude Code's cleanup period: 30 days).
const TranscriptRetention = 30 * 24 * time.Hour

// Transcript is the saved work of one sub-agent. It is kept apart from the
// main conversation, so it survives that conversation being compacted or
// cleared, and lets the sub-agent be resumed later with its full history.
type Transcript struct {
	ID          string        `json:"id"`
	Agent       string        `json:"agent"`
	Model       string        `json:"model"`
	Description string        `json:"description"`
	Status      string        `json:"status"` // finished, partial, cancelled, error
	Messages    []llm.Message `json:"messages"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

var agentIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NewAgentID returns a unique, file-name safe id for a new sub-agent.
func NewAgentID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "a" + time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func transcriptPath(dir, id string) (string, error) {
	if dir == "" {
		return "", errors.New("sub-agent work is not being kept")
	}
	if !agentIDRe.MatchString(id) {
		return "", fmt.Errorf("invalid agent_id %q", id)
	}
	return filepath.Join(dir, id+".json"), nil
}

// SaveTranscript writes t atomically to dir.
func SaveTranscript(dir string, t Transcript) error {
	p, err := transcriptPath(dir, t.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// LoadTranscript reads the saved work of sub-agent id.
func LoadTranscript(dir, id string) (Transcript, error) {
	var t Transcript
	p, err := transcriptPath(dir, id)
	if err != nil {
		return t, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return t, fmt.Errorf("no sub-agent with agent_id %q (it may have expired)", id)
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("corrupt sub-agent record %q: %w", id, err)
	}
	if len(t.Messages) == 0 {
		return t, fmt.Errorf("sub-agent %q has no saved history", id)
	}
	return t, nil
}

// PruneTranscripts deletes saved sub-agent work not updated within maxAge and
// returns how many records it removed.
func PruneTranscripts(dir string, maxAge time.Duration) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}
