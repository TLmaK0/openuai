package procs

import (
	"regexp"
	"strings"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[PX^_][^\x1b]*\x1b\\|\x1b[()*+][0-9A-Za-z]|\x1b[@-Z\\-_=>78]`)

// Clean turns raw terminal output into plain text: escape sequences are
// removed, carriage-return redraws (progress bars) keep only their final
// state and backspaces are applied.
func Clean(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.Contains(l, "\r") {
			last := ""
			for _, part := range strings.Split(l, "\r") {
				if part != "" {
					last = part
				}
			}
			l = last
		}
		out := make([]rune, 0, len(l))
		for _, r := range l {
			switch {
			case r == '\b':
				if len(out) > 0 {
					out = out[:len(out)-1]
				}
			case r == '\t':
				out = append(out, r)
			case r < 0x20 || r == 0x7f:
			default:
				out = append(out, r)
			}
		}
		lines[i] = string(out)
	}
	return strings.Join(lines, "\n")
}

var redactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED PRIVATE KEY]"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[REDACTED]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{16,}`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)\b((?:password|passwd|secret|token|api[_-]?key)\s*[:=]\s*)[^\s"']{6,}`), "${1}[REDACTED]"},
}

// Redact hides well-known secret formats (tokens, keys) so they never reach
// the language model.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

var promptWords = []string{
	"password", "passphrase", "passcode", "[y/n]", "(y/n)", "y/n]", "(yes/no",
	"press enter", "press any key", "press return",
}

// LooksLikePrompt guesses whether raw terminal output ends with a question
// for the user (a password prompt, a Y/n question, an interactive menu...).
func LooksLikePrompt(raw string) bool {
	var lines []string
	for _, l := range strings.Split(Clean(raw), "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) == 0 {
		return false
	}
	last := lines[len(lines)-1]
	low := strings.ToLower(last)
	for _, w := range promptWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	if strings.HasSuffix(last, ":") || strings.HasSuffix(last, "?") || strings.HasSuffix(last, ">") ||
		strings.HasSuffix(last, "$") || strings.HasSuffix(last, "#") || strings.HasSuffix(last, "›") ||
		strings.HasSuffix(last, "»") {
		return true
	}
	// Interactive menus (inquirer style): "? Question" followed by options.
	from := len(lines) - 8
	if from < 0 {
		from = 0
	}
	for _, l := range lines[from:] {
		if strings.HasPrefix(l, "? ") {
			return true
		}
	}
	return false
}
