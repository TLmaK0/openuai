package hooks

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Claude Code tool names mapped to the OpenUAI tools they correspond to, so
// `if` rules written for Claude Code (Bash(git *), Edit(*.ts)) work here.
var claudeTools = map[string][]string{
	"Bash":         {"bash", "bash_sudo"},
	"PowerShell":   {"bash", "bash_sudo"},
	"Write":        {"write_file"},
	"Edit":         {"write_file"},
	"MultiEdit":    {"write_file"},
	"NotebookEdit": {"write_file"},
	"Read":         {"read_file"},
	"Glob":         {"search_files", "list_dir"},
	"Grep":         {"search_files"},
	"LS":           {"list_dir"},
	"WebFetch":     {"web_fetch"},
	"Agent":        {"spawn_agents"},
	"Task":         {"spawn_agents"},
	"Skill":        {"skill"},
}

func toolNameMatches(name, tool string) bool {
	return name == tool || contains(claudeTools[name], tool)
}

// File tools and the input fields holding their paths.
var pathArgs = map[string][]string{
	"read_file": {"path"}, "write_file": {"path"}, "delete_file": {"path"}, "list_dir": {"path"},
	"search_files": {"path", "pattern"}, "move_file": {"source", "destination"},
}

// ifMatches evaluates an `if` permission rule such as "Bash(git *)",
// "write_file(*.go)" or "Read" against the tool call in input. Like Claude
// Code it is best-effort: when the command cannot be parsed the hook runs.
func ifMatches(rule string, input map[string]any, dir string) bool {
	rule = strings.TrimSpace(rule)
	name, pattern := rule, ""
	if i := strings.IndexByte(rule, '('); i > 0 && strings.HasSuffix(rule, ")") {
		name, pattern = strings.TrimSpace(rule[:i]), strings.TrimSpace(rule[i+1:len(rule)-1])
	}
	tool, _ := input["tool_name"].(string)
	if !toolNameMatches(name, tool) {
		return false
	}
	if pattern == "" || pattern == "*" {
		return true
	}
	args, _ := input["tool_input"].(map[string]any)
	str := func(k string) string { s, _ := args[k].(string); return s }
	switch {
	case tool == "bash" || tool == "bash_sudo":
		return bashMatches(pattern, str("command"))
	case pathArgs[tool] != nil:
		for _, k := range pathArgs[tool] {
			if p := str(k); p != "" && pathMatches(pattern, p, dir) {
				return true
			}
		}
		return false
	case tool == "web_fetch":
		u := str("url")
		if d, ok := strings.CutPrefix(pattern, "domain:"); ok {
			pu, err := url.Parse(u)
			return err == nil && (pu.Hostname() == d || strings.HasSuffix(pu.Hostname(), "."+d))
		}
		return wildcard(pattern).MatchString(u)
	}
	// Any other tool: the pattern is matched against its string arguments.
	re := wildcard(pattern)
	for _, v := range args {
		if s, ok := v.(string); ok && re.MatchString(s) {
			return true
		}
	}
	return false
}

// wildcard turns a pattern where * matches anything into an anchored regexp.
func wildcard(p string) *regexp.Regexp {
	parts := strings.Split(p, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^(?s)" + strings.Join(parts, ".*") + "$")
}

var leadingAssign = regexp.MustCompile(`^(\s*[A-Za-z_][A-Za-z0-9_]*=('[^']*'|"[^"]*"|\S*)\s+)+`)

// bashMatches checks a Bash pattern against every subcommand of cmd
// (split on && || ; | and newlines, plus those inside $() and backticks),
// with leading VAR=value assignments stripped.
func bashMatches(pattern, cmd string) bool {
	cmds, subst, ok := splitBash(cmd)
	if !ok {
		return true // cannot tell what it runs: run the hook
	}
	// A pattern that specifies more than the command name runs anyway when
	// the command has $(), backticks or $VAR (their value is unknown).
	if subst {
		if f := strings.Fields(strings.TrimSuffix(pattern, ":*")); len(f) > 1 && !(len(f) == 2 && f[1] == "*") {
			return true
		}
	}
	prefix, isPrefix := strings.CutSuffix(pattern, ":*")
	re := wildcard(pattern)
	bare := ""
	if strings.HasSuffix(pattern, " *") {
		bare = strings.TrimSuffix(pattern, " *")
	}
	for _, c := range cmds {
		c = strings.TrimSpace(leadingAssign.ReplaceAllString(c, ""))
		switch {
		case isPrefix && (c == prefix || strings.HasPrefix(c, prefix+" ")):
			return true
		case !isPrefix && (re.MatchString(c) || (bare != "" && c == bare)):
			return true
		}
	}
	return false
}

// splitBash splits a shell command into its simple commands. subst reports
// command substitution or variable expansion; ok is false when the command
// cannot be parsed (unbalanced quotes or parentheses).
func splitBash(s string) (cmds []string, subst, ok bool) {
	var cur strings.Builder
	sq, dq := false, false
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			cmds = append(cmds, t)
		}
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case sq:
			if c == '\'' {
				sq = false
			}
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(c)
			cur.WriteByte(s[i+1])
			i++
		case c == '\'' && !dq:
			sq = true
			cur.WriteByte(c)
		case c == '"':
			dq = !dq
			cur.WriteByte(c)
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			depth, j := 0, i+1
			for ; j < len(s); j++ {
				if s[j] == '(' {
					depth++
				} else if s[j] == ')' {
					if depth--; depth == 0 {
						break
					}
				}
			}
			if j >= len(s) {
				return nil, false, false
			}
			inner, _, ok := splitBash(s[i+2 : j])
			if !ok {
				return nil, false, false
			}
			cmds = append(cmds, inner...)
			subst = true
			cur.WriteString(s[i : j+1])
			i = j
		case c == '`':
			j := strings.IndexByte(s[i+1:], '`')
			if j < 0 {
				return nil, false, false
			}
			inner, _, ok := splitBash(s[i+1 : i+1+j])
			if !ok {
				return nil, false, false
			}
			cmds = append(cmds, inner...)
			subst = true
			cur.WriteString(s[i : i+2+j])
			i += 1 + j
		case c == '$' && i+1 < len(s) && (s[i+1] == '{' || s[i+1] == '_' || isLetter(s[i+1])):
			subst = true
			cur.WriteByte(c)
		case !dq && c == '&' && ((i > 0 && (s[i-1] == '>' || s[i-1] == '<')) || (i+1 < len(s) && s[i+1] == '>')):
			cur.WriteByte(c) // redirection (2>&1, &>file)
		case !dq && (c == ';' || c == '\n' || c == '|' || c == '&'):
			flush()
			if i+1 < len(s) && (s[i+1] == '&' || s[i+1] == '|') {
				i++
			}
		default:
			cur.WriteByte(c)
		}
	}
	if sq || dq {
		return nil, false, false
	}
	flush()
	return cmds, subst, true
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// pathMatches checks a file pattern of a permission rule: one without a
// slash (*.go) matches the file name anywhere; one with a slash is relative
// to dir (//abs is absolute, ~/ is the home directory); ** spans
// directories.
func pathMatches(pattern, p, dir string) bool {
	if !filepath.IsAbs(p) && !strings.HasPrefix(p, "~") {
		p = filepath.Join(dir, p)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		}
		if strings.HasPrefix(pattern, "~/") {
			pattern = filepath.ToSlash(home) + pattern[1:]
		}
	}
	p = filepath.ToSlash(filepath.Clean(p))
	if !strings.Contains(pattern, "/") {
		return pathGlob(pattern).MatchString(p[strings.LastIndex(p, "/")+1:])
	}
	switch {
	case strings.HasPrefix(pattern, "//"):
		pattern = pattern[1:]
	case strings.HasPrefix(pattern, "/"):
	default:
		pattern = strings.TrimSuffix(filepath.ToSlash(dir), "/") + "/" + strings.TrimPrefix(pattern, "./")
	}
	return pathGlob(pattern).MatchString(p)
}

// pathGlob compiles a path glob: ** spans directories, * and ? stay inside
// one path segment; a trailing /** also matches the directory itself.
func pathGlob(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	tail := ""
	if strings.HasSuffix(g, "/**") {
		g, tail = strings.TrimSuffix(g, "/**"), "(?:/.*)?"
	}
	for i := 0; i < len(g); i++ {
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case g[i] == '*':
			b.WriteString("[^/]*")
		case g[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(g[i])))
		}
	}
	b.WriteString(tail + "$")
	return regexp.MustCompile(b.String())
}
