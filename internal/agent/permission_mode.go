package agent

import (
	"context"

	"openuai/internal/hooks"
	"openuai/internal/logger"
)

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
func (a *Agent) allowTool(ctx context.Context, name, required, desc string) (bool, string) {
	d := a.decidePermission(ctx, name, required, desc, nil, false)
	return d.ok, d.why
}

// permDecision is the outcome of the permission flow for one tool call.
type permDecision struct {
	ok     bool
	why    string            // reason for a denial (for the tool result)
	args   map[string]string // input changed by a PermissionRequest hook
	byHook bool              // a hook decided: no PermissionDenied event
}

// decidePermission runs the permission flow: the permission mode, then the
// PermissionRequest hooks right before the user would be asked, then the
// dialog. force asks even for a tool that is already allowed (a PreToolUse
// hook answered "ask").
func (a *Agent) decidePermission(ctx context.Context, name, required, desc string, args map[string]string, force bool) permDecision {
	switch a.permMode {
	case PermModePlan:
		if required != "none" && required != "" {
			logger.Info("Plan mode: denied %s", name)
			return permDecision{why: "This agent runs in plan mode (read-only): only tools that change nothing are allowed."}
		}
		return permDecision{ok: true}
	case PermModeDontAsk:
		if !force && a.permissions.Allowed(name, required) {
			return permDecision{ok: true}
		}
		// Nobody can be asked, but PermissionRequest hooks still decide.
		if d, decided := a.permissionRequest(ctx, name, args); decided {
			return d
		}
		logger.Info("dontAsk mode: denied %s", name)
		return permDecision{why: "This agent runs in dontAsk mode: actions that would need the user's approval are denied."}
	case PermModeAcceptEdits:
		if !force && acceptEditTools[name] {
			return permDecision{ok: true}
		}
	}
	if !force && a.permissions.Allowed(name, required) {
		return permDecision{ok: true}
	}
	if d, decided := a.permissionRequest(ctx, name, args); decided {
		return d
	}
	if a.permissions.check(ctx, name, required, desc, force) {
		return permDecision{ok: true}
	}
	return permDecision{why: "The user denied this action."}
}

// permissionRequest runs the PermissionRequest hooks before the user is
// asked. decided is false when no hook allowed or denied the call.
func (a *Agent) permissionRequest(ctx context.Context, name string, args map[string]string) (permDecision, bool) {
	if !a.hookSet().Has(hooks.PermissionRequest) {
		return permDecision{}, false
	}
	r := a.runHook(ctx, hooks.PermissionRequest, name, map[string]any{
		"tool_name": name, "tool_input": argsAny(args),
		"permission_suggestions": []map[string]any{{
			"type": "addRules", "rules": []map[string]any{{"toolName": name}},
			"behavior": "allow", "destination": "session",
		}},
	})
	switch {
	case a.halt != "":
		return permDecision{why: "The turn was stopped by a hook: " + a.halt, byHook: true}, true
	case r.Decision == "allow":
		logger.Info("PermissionRequest hook allowed %s", name)
		d := permDecision{ok: true, byHook: true}
		if r.UpdatedInput != nil {
			d.args = argsString(r.UpdatedInput)
		}
		return d, true
	case r.Decision == "deny":
		why := r.Reason
		if why == "" || why == "blocked by a hook" {
			why = "denied by a PermissionRequest hook"
		}
		logger.Info("PermissionRequest hook denied %s: %s", name, why)
		return permDecision{why: why, byHook: true}, true
	}
	return permDecision{}, false
}
