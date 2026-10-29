package agent

import "openuai/internal/logger"

// Permission modes a sub-agent can run in (from its definition's
// permissionMode). The empty mode is the normal one: ask the user as usual.
const (
	PermModeDefault     = ""
	PermModeAcceptEdits = "acceptEdits"
	PermModeDontAsk     = "dontAsk"
	PermModePlan        = "plan"
)

// acceptEditTools are approved without asking in acceptEdits mode: file
// edits and moves. Deleting files and running commands still ask.
var acceptEditTools = map[string]bool{"write_file": true, "move_file": true}

// Allowed reports whether a tool may run without asking the user: it needs
// no permission, or the user already allowed it (for the session or forever).
func (pm *PermissionManager) Allowed(toolName, requiredPerm string) bool {
	if requiredPerm == "none" || requiredPerm == "" {
		return true
	}
	if pm == nil {
		return false
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.foreverAllowed[toolName] {
		return true
	}
	return requiredPerm == "session" && pm.sessionAllowed[toolName]
}

// allowTool decides whether a tool call may run under the agent's permission
// mode. When it may not, the reason is returned for the tool result.
func (a *Agent) allowTool(name, required, desc string) (bool, string) {
	switch a.permMode {
	case PermModePlan:
		if required != "none" && required != "" {
			logger.Info("Plan mode: denied %s", name)
			return false, "This agent runs in plan mode (read-only): only tools that change nothing are allowed."
		}
		return true, ""
	case PermModeDontAsk:
		if a.permissions.Allowed(name, required) {
			return true, ""
		}
		logger.Info("dontAsk mode: denied %s", name)
		return false, "This agent runs in dontAsk mode: actions that would need the user's approval are denied."
	case PermModeAcceptEdits:
		if acceptEditTools[name] {
			return true, ""
		}
	}
	if a.permissions.Check(name, required, desc) {
		return true, ""
	}
	return false, "The user denied this action."
}
