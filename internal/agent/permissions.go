package agent

import (
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
	mu              sync.Mutex
	sessionAllowed  map[string]bool          // tools allowed for this session
	foreverAllowed  map[string]bool          // tools permanently allowed
	configPath      string
	requestApproval func(tool, command string) (PermissionLevel, bool) // UI callback
}

type permConfig struct {
	ForeverAllowed []string `json:"forever_allowed"`
}

func NewPermissionManager(configDir string, requestApproval func(tool, command string) (PermissionLevel, bool)) *PermissionManager {
	pm := &PermissionManager{
		sessionAllowed:  make(map[string]bool),
		foreverAllowed:  make(map[string]bool),
		configPath:      filepath.Join(configDir, "permissions.json"),
		requestApproval: requestApproval,
	}
	pm.load()
	return pm
}

func (pm *PermissionManager) Check(toolName string, requiredPerm string, commandDesc string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// No permission needed
	if requiredPerm == "none" || requiredPerm == "" {
		return true
	}

	// Permanently allowed
	if pm.foreverAllowed[toolName] {
		return true
	}

	// Session allowed (only for "session" level tools)
	if requiredPerm == "session" && pm.sessionAllowed[toolName] {
		return true
	}

	// Need to ask the user
	if pm.requestApproval == nil {
		return false
	}

	level, approved := pm.requestApproval(toolName, commandDesc)
	if !approved {
		return false
	}

	switch level {
	case PermSession:
		pm.sessionAllowed[toolName] = true
	case PermForever:
		pm.foreverAllowed[toolName] = true
		pm.save()
	}

	return true
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
