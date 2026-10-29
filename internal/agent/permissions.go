package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// PermissionLevel defines how a tool's permission is managed
// "none"    = always allowed, no confirmation needed
// "session" = ask once per session, then allowed until restart
// "always"  = ask every time
// "forever" = permanently allowed (persisted to config)
type PermissionLevel string

const (
	PermNone    PermissionLevel = "none"
	PermSession PermissionLevel = "session"
	PermAlways  PermissionLevel = "always"
	PermForever PermissionLevel = "forever"
)

type PermissionManager struct {
	mu             sync.Mutex
	sessionAllowed map[string]bool // tools allowed for this session
	foreverAllowed map[string]bool // tools permanently allowed
	configPath     string
	// requestApproval asks the user; it must return (deny) when ctx is done.
	requestApproval func(ctx context.Context, tool, command string) (PermissionLevel, bool)
	// ask allows a single approval dialog at a time. It is a channel, not a
	// mutex, so waiting for it can be cancelled (Stop / Esc).
	ask chan struct{}
}

type permConfig struct {
	ForeverAllowed []string `json:"forever_allowed"`
}

func NewPermissionManager(configDir string, requestApproval func(ctx context.Context, tool, command string) (PermissionLevel, bool)) *PermissionManager {
	pm := &PermissionManager{
		sessionAllowed:  make(map[string]bool),
		foreverAllowed:  make(map[string]bool),
		configPath:      filepath.Join(configDir, "permissions.json"),
		requestApproval: requestApproval,
		ask:             make(chan struct{}, 1),
	}
	pm.load()
	return pm
}

// Check reports whether a tool call may run, asking the user when needed.
// The lock is never held while waiting for the user, so other agents (and
// tools that need no permission) keep working while a dialog is open, and
// cancelling ctx (Stop / Esc) ends the wait at once with a denial.
func (pm *PermissionManager) Check(ctx context.Context, toolName string, requiredPerm string, commandDesc string) bool {
	if pm.allowedNow(toolName, requiredPerm) {
		return true
	}
	if pm.requestApproval == nil {
		return false
	}
	select {
	case pm.ask <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-pm.ask }()
	// Another agent may have been allowed it while we waited for the dialog.
	if pm.allowedNow(toolName, requiredPerm) {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	level, approved := pm.requestApproval(ctx, toolName, commandDesc)
	if !approved || ctx.Err() != nil {
		return false
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	switch level {
	case PermSession:
		pm.sessionAllowed[toolName] = true
	case PermForever:
		pm.foreverAllowed[toolName] = true
		pm.save()
	}
	return true
}

// allowedNow reports whether the tool needs no question right now.
func (pm *PermissionManager) allowedNow(toolName, requiredPerm string) bool {
	if requiredPerm == "none" || requiredPerm == "" {
		return true
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.foreverAllowed[toolName] {
		return true
	}
	return requiredPerm == "session" && pm.sessionAllowed[toolName]
}

// AllowForSession allows a tool for the current session
func (pm *PermissionManager) AllowForSession(toolName string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.sessionAllowed[toolName] = true
}

// AllowForever permanently allows a tool
func (pm *PermissionManager) AllowForever(toolName string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.foreverAllowed[toolName] = true
	pm.save()
}

// RevokeForever removes a permanent permission
func (pm *PermissionManager) RevokeForever(toolName string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.foreverAllowed, toolName)
	pm.save()
}

// GetForeverAllowed returns all permanently allowed tools
func (pm *PermissionManager) GetForeverAllowed() []string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	out := make([]string, 0, len(pm.foreverAllowed))
	for k := range pm.foreverAllowed {
		out = append(out, k)
	}
	return out
}

func (pm *PermissionManager) load() {
	data, err := os.ReadFile(pm.configPath)
	if err != nil {
		return
	}
	var cfg permConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return
	}
	for _, name := range cfg.ForeverAllowed {
		pm.foreverAllowed[name] = true
	}
}

func (pm *PermissionManager) save() {
	cfg := permConfig{
		ForeverAllowed: make([]string, 0, len(pm.foreverAllowed)),
	}
	for name := range pm.foreverAllowed {
		cfg.ForeverAllowed = append(cfg.ForeverAllowed, name)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(pm.configPath), 0o700)
	os.WriteFile(pm.configPath, data, 0o600)
}
