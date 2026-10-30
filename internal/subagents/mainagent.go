package subagents

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"openuai/internal/hooks"
	"openuai/internal/tools"
)

// Running the main conversation as an agent definition (Claude Code's
// --agent / "agent" setting). The project setting lives in
// .openuai/settings.json (shared) and .openuai/settings.local.json (not for
// git) of each directory from the repository root down to the working
// directory; the closest file wins and the local file wins over the shared
// one in the same directory.

// SpawnTool is the OpenUAI tool that launches sub-agents.
const SpawnTool = "spawn_agents"

// spawnAliases are the names a definition's tools may use for spawn_agents
// (Claude Code's Agent, and its old name Task).
var spawnAliases = []string{"agent", "task", SpawnTool}

// spawnEntry reports whether a tools entry names the sub-agent tool, and the
// types listed in parentheses (Agent(worker, researcher)), if any.
func spawnEntry(entry string) (bool, []string, bool) {
	e := strings.TrimSpace(entry)
	name, args, hasArgs := e, "", false
	if i := strings.Index(e, "("); i >= 0 && strings.HasSuffix(e, ")") {
		name, args, hasArgs = strings.TrimSpace(e[:i]), e[i+1:len(e)-1], true
	}
	for _, a := range spawnAliases {
		if strings.EqualFold(name, a) {
			if !hasArgs {
				return true, nil, false
			}
			return true, splitList(args), true
		}
	}
	return false, nil, false
}

// toolPattern maps an entry of tools to the OpenUAI tool name it matches:
// Agent, Task, Agent(x, y) and spawn_agents(x) all mean spawn_agents.
func toolPattern(entry string) string {
	if ok, _, _ := spawnEntry(entry); ok {
		return SpawnTool
	}
	return entry
}

// SpawnAllowlist returns the sub-agent types d may launch when it runs as
// the main conversation. restricted is false when any type may be launched
// (no tools field, or a bare Agent/spawn_agents entry). Whether d gets the
// tool at all is decided by FilterMainTools.
func SpawnAllowlist(d *Definition) (types []string, restricted bool) {
	if d == nil || !d.HasTools {
		return nil, false
	}
	seen := map[string]bool{}
	for _, e := range d.Tools {
		ok, list, hasArgs := spawnEntry(e)
		if !ok {
			continue
		}
		if !hasArgs {
			return nil, false
		}
		restricted = true
		for _, t := range list {
			if !seen[t] {
				seen[t] = true
				types = append(types, t)
			}
		}
	}
	sort.Strings(types)
	return types, restricted
}

// FilterMainTools is FilterTools for an agent running as the main
// conversation: the tools only the main conversation has (watch_chat...)
// stay available when the definition allows them.
func FilterMainTools(reg *tools.Registry, d *Definition) (*tools.Registry, error) {
	return filterTools(reg, d, nil)
}

// MainHookSet returns the hooks of d for the main conversation (Stop stays
// Stop), or nil when it has none.
func MainHookSet(d *Definition) *hooks.Set {
	if d == nil || len(d.MainHooks) == 0 {
		return nil
	}
	s := &hooks.Set{}
	s.Add(d.MainHooks, "agent:"+d.Name)
	return s
}

// Project settings files, relative to a project directory.
var (
	ProjectSettingsFile      = filepath.Join(".openuai", "settings.json")
	ProjectLocalSettingsFile = filepath.Join(".openuai", "settings.local.json")
)

// readAgentSetting returns the "agent" key of a settings file; present is
// false when the file or the key does not exist.
func readAgentSetting(path string) (name string, present bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return "", false, fmt.Errorf("%s: invalid JSON: %v", path, err)
	}
	raw, ok := m["agent"]
	if !ok {
		return "", false, nil
	}
	if err := json.Unmarshal(raw, &name); err != nil {
		return "", false, fmt.Errorf("%s: \"agent\" must be a string", path)
	}
	return strings.TrimSpace(name), true, nil
}

// ProjectAgentSetting returns the agent the project at workDir sets for the
// main conversation and the file that sets it ("" and "" when none). An
// empty "agent" value means the normal assistant and still counts as set.
func ProjectAgentSetting(workDir string) (name, path string, err error) {
	chain := projectChain(workDir)
	for i := len(chain) - 1; i >= 0; i-- {
		for _, f := range []string{ProjectLocalSettingsFile, ProjectSettingsFile} {
			p := filepath.Join(chain[i], f)
			n, ok, err := readAgentSetting(p)
			if err != nil {
				return "", p, err
			}
			if ok {
				return n, p, nil
			}
		}
	}
	return "", "", nil
}

// SaveProjectAgentSetting writes (or, with an empty name, removes) the
// "agent" key of <workDir>/.openuai/settings.json, keeping its other keys.
// It returns the file written.
func SaveProjectAgentSetting(workDir, name string) (string, error) {
	if workDir == "" {
		return "", errors.New("no project directory")
	}
	p := filepath.Join(workDir, ProjectSettingsFile)
	m := map[string]json.RawMessage{}
	if data, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(data, &m); err != nil {
			return p, fmt.Errorf("%s: invalid JSON: %v", p, err)
		}
		if m == nil {
			m = map[string]json.RawMessage{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return p, err
	}
	if name == "" {
		if _, ok := m["agent"]; !ok {
			return p, nil
		}
		delete(m, "agent")
	} else {
		raw, _ := json.Marshal(name)
		m["agent"] = raw
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return p, err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return p, err
	}
	return p, os.WriteFile(p, append(data, '\n'), 0o644)
}

// MainAgentPrompt is the system prompt of an agent running as the main
// conversation: the definition's body replaces the default prompt (as
// Claude Code's --agent does); with an empty body the default prompt stays.
// The instruction files (AGENTS.md) and the environment are added by the
// agent package, as for the normal assistant.
func MainAgentPrompt(d *Definition, defaultPrompt string) string {
	if d != nil && strings.TrimSpace(d.Prompt) != "" {
		return d.Prompt
	}
	return defaultPrompt
}
