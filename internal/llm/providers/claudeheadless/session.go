package claudeheadless

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"openuai/internal/llm"
)

// Each headless run used to be a brand-new CLI session fed the whole
// conversation as one message. The prompt cache only matches whole blocks, so
// every call rewrote the cache for the entire conversation (about $3 per call
// on a long session). Now a conversation keeps one CLI session: the first call
// creates it with --session-id, later calls --resume it and send only the
// messages added since, so the earlier part is read from the cache.

// prunedStub is what the agent loop puts in place of old tool results. The
// CLI session still holds the original, so a pruned result is not a change.
const prunedStub = "[Old tool result cleared]"

const (
	sessionIdle = 2 * time.Hour
	maxSessions = 64
)

type cliSession struct {
	id        string
	sent      []llm.Message // conversation as of the last successful call
	lastTotal float64       // CLI's cumulative total_cost_usd after that call
	busy      bool
	used      time.Time
}

type sessionStore struct {
	mu sync.Mutex
	m  map[string]*cliSession
}

func newSessionStore() *sessionStore { return &sessionStore{m: map[string]*cliSession{}} }

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// conversationKey identifies a conversation: same mode, model, tools, system
// message and first user message.
func conversationKey(mode, model string, messages []llm.Message, tools []llm.ToolDefinition) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", mode, model)
	sorted := append([]llm.ToolDefinition(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	tj, _ := json.Marshal(sorted)
	h.Write(tj)
	for i, m := range messages {
		if i == 0 || m.Role == llm.RoleUser {
			fmt.Fprintf(h, "\x00%s\x00%s", m.Role, m.Content)
		}
		if m.Role == llm.RoleUser {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sameMessage(old, cur llm.Message) bool {
	if old.Role != cur.Role || old.ToolCallID != cur.ToolCallID {
		return false
	}
	if len(old.ToolCalls) != 0 || len(cur.ToolCalls) != 0 {
		if !reflect.DeepEqual(old.ToolCalls, cur.ToolCalls) {
			return false
		}
	}
	if old.Content == cur.Content {
		return true
	}
	return cur.Role == llm.RoleToolResult && cur.Content == prunedStub
}

// continuation reports whether cur only adds messages after prev.
func continuation(prev, cur []llm.Message) bool {
	if len(prev) == 0 || len(cur) <= len(prev) {
		return false
	}
	for i := range prev {
		if !sameMessage(prev[i], cur[i]) {
			return false
		}
	}
	return true
}

// acquire returns the CLI session for the conversation and whether it can be
// resumed with only the new messages.
func (st *sessionStore) acquire(key string, cur []llm.Message) (*cliSession, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	st.evictLocked(now)
	if s, ok := st.m[key]; ok {
		if s.busy {
			// Same conversation already running: use a separate, untracked session.
			return &cliSession{id: newSessionID(), busy: true, used: now}, false
		}
		s.busy, s.used = true, now
		if continuation(s.sent, cur) {
			return s, true
		}
		// History changed (compaction, edited message): start over.
		s.id, s.sent, s.lastTotal = newSessionID(), nil, 0
		return s, false
	}
	s := &cliSession{id: newSessionID(), busy: true, used: now}
	st.m[key] = s
	return s, false
}

func (st *sessionStore) release(s *cliSession, cur []llm.Message, total float64, ok bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s.busy, s.used = false, time.Now()
	if !ok {
		s.id, s.sent, s.lastTotal = newSessionID(), nil, 0
		return
	}
	s.sent = append([]llm.Message(nil), cur...)
	s.lastTotal = total
}

func (st *sessionStore) evictLocked(now time.Time) {
	for k, s := range st.m {
		if !s.busy && now.Sub(s.used) > sessionIdle {
			delete(st.m, k)
		}
	}
	for len(st.m) >= maxSessions {
		oldest := ""
		for k, s := range st.m {
			if !s.busy && (oldest == "" || s.used.Before(st.m[oldest].used)) {
				oldest = k
			}
		}
		if oldest == "" {
			return
		}
		delete(st.m, oldest)
	}
}

// callCost turns the CLI's total_cost_usd into the cost of this call: on a
// resumed session the CLI reports the session's cumulative total.
func callCost(total, previous float64, resumed bool) float64 {
	if !resumed || total < previous {
		return total
	}
	return total - previous
}
