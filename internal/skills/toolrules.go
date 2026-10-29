package skills

import (
	"regexp"
	"strings"
)

// ToolRule is one allowed-tools / disallowed-tools entry in OpenUAI terms: a
// tool name and an optional pattern for its main argument (the command for
// bash, the path for file tools).
type ToolRule struct {
	Tool    string
	Pattern string
}

// claudeTools maps Claude Code tool names to OpenUAI's.
var claudeTools = map[string][]string{
	"bash":         {"bash"},
	"powershell":   {"bash"},
	"read":         {"read_file"},
	"write":        {"write_file"},
	"edit":         {"write_file"},
	"multiedit":    {"write_file"},
	"notebookedit": {"write_file"},
	"glob":         {"search_files"},
	"grep":         {"search_files"},
	"ls":           {"list_dir"},
	"webfetch":     {"web_fetch"},
	"task":         {"spawn_agents"},
	"agent":        {"spawn_agents"},
	"skill":        {"skill"},
}

// gitTools are the OpenUAI git tools a Bash(git <sub> ...) rule also covers.
var gitTools = map[string]string{"status": "git_status", "diff": "git_diff", "log": "git_log", "add": "git_add", "commit": "git_commit", "branch": "git_branch"}

var ruleRe = regexp.MustCompile(`^([A-Za-z0-9_.:-]+)(?:\((.*)\))?$`)

// ToolRules turns the skill's allowed-tools (or disallowed-tools) entries
// into OpenUAI rules. ${OPENUAI_SKILL_DIR} and ${OPENUAI_PROJECT_DIR} are
// substituted in the patterns.
func ToolRules(entries []string, sk Skill, projectDir string) []ToolRule {
	vars := strings.NewReplacer("${OPENUAI_SKILL_DIR}", sk.Dir, "${OPENUAI_PROJECT_DIR}", projectDir)
	var out []ToolRule
	for _, e := range entries {
		m := ruleRe.FindStringSubmatch(strings.TrimSpace(e))
		if m == nil {
			continue
		}
		name, pattern := m[1], strings.TrimSpace(vars.Replace(m[2]))
		// Claude Code's older prefix syntax: Bash(git:*) is Bash(git *).
		if strings.HasSuffix(pattern, ":*") {
			pattern = strings.TrimSuffix(pattern, ":*") + " *"
		}
		for _, t := range MapToolName(name) {
			out = append(out, ToolRule{Tool: t, Pattern: pattern})
			if t == "bash" {
				if f := strings.Fields(pattern); len(f) >= 2 && f[0] == "git" {
					if g, ok := gitTools[f[1]]; ok {
						out = append(out, ToolRule{Tool: g})
					}
				}
			}
		}
	}
	return out
}

// MapToolName returns the OpenUAI tools a Claude Code tool name stands for.
// MCP tools (mcp__server__tool) become mcp_server_tool; other names are
// kept, so OpenUAI names work as they are.
func MapToolName(name string) []string {
	if t, ok := claudeTools[strings.ToLower(name)]; ok {
		return t
	}
	if strings.HasPrefix(name, "mcp__") {
		return []string{"mcp_" + strings.ReplaceAll(strings.TrimPrefix(name, "mcp__"), "__", "_")}
	}
	return []string{name}
}

// Matches reports whether the rule covers a call to tool whose main
// argument is arg. In patterns * matches anything.
func (r ToolRule) Matches(tool, arg string) bool {
	if r.Tool != tool && !(strings.HasSuffix(r.Tool, "*") && strings.HasPrefix(tool, strings.TrimSuffix(r.Tool, "*"))) {
		return false
	}
	if r.Pattern == "" || r.Pattern == "*" {
		return true
	}
	parts := strings.Split(r.Pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	expr := "^" + strings.Join(parts, ".*") + "$"
	// "git *" also matches a bare "git".
	if strings.HasSuffix(r.Pattern, " *") {
		expr = "^" + strings.Join(parts[:len(parts)-1], ".*")
		expr = strings.TrimSuffix(expr, " ") + "(?: .*)?$"
	}
	re, err := regexp.Compile(expr)
	return err == nil && re.MatchString(strings.TrimSpace(arg))
}

// AnyMatch reports whether one of rules covers the call.
func AnyMatch(rules []ToolRule, tool, arg string) bool {
	for _, r := range rules {
		if r.Matches(tool, arg) {
			return true
		}
	}
	return false
}

// ToolNames returns the tool names of rules without a pattern (what
// disallowed-tools removes from the pool).
func ToolNames(rules []ToolRule) []string {
	var out []string
	for _, r := range rules {
		if r.Pattern == "" {
			out = append(out, r.Tool)
		}
	}
	return out
}
