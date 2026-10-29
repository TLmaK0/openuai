package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ShellDisabledText replaces each injected command when shell execution is
// turned off (disable_skill_shell_execution).
const ShellDisabledText = "[shell command execution disabled by policy]"

// RenderOptions are the values and services Render needs.
type RenderOptions struct {
	ProjectDir string
	SessionID  string
	Effort     string
	// Run runs an injected command (nil: the default runner, see RunShell).
	Run func(command, shell string) ShellResult
	// CanRun reports whether an injected command may run without asking
	// (nil: every command may run). Injected commands never prompt.
	CanRun func(command string) bool
	// ShellDisabled replaces every injected command with ShellDisabledText.
	ShellDisabled bool
}

// Render returns the skill's instructions ready for the model: arguments
// and variables substituted, !`command` and ```! blocks replaced by their
// output, files named with @path attached, and supporting files listed. An
// injected command that fails or may not run aborts the whole invocation.
func Render(sk Skill, args string, o RenderOptions) (string, error) {
	parts := splitArgs(args)
	used := false
	sub := func(s string) string {
		s = substituteArgs(s, args, parts, sk.Arguments, &used)
		return substituteVars(s, sk, o)
	}

	segs := segment(sk.Body)
	if hasCommands(segs) && !o.ShellDisabled && o.Run == nil {
		if err := shellAvailable(sk); err != nil {
			return "", err
		}
	}
	var body strings.Builder
	var refs []string
	for _, sg := range segs {
		if sg.cmd == "" {
			body.WriteString(sub(sg.text))
			refs = append(refs, fileRefs(substituteVars(sg.text, sk, o))...)
			continue
		}
		command := sub(sg.cmd)
		if o.ShellDisabled {
			body.WriteString(ShellDisabledText)
			continue
		}
		if o.CanRun != nil && !o.CanRun(command) {
			return "", fmt.Errorf("Shell command permission check failed for pattern %q: allow it with the skill's allowed-tools (e.g. Bash(%s)) or allow bash for the session", sg.pattern, firstWord(command)+" *")
		}
		run := o.Run
		if run == nil {
			run = func(c, sh string) ShellResult { return RunShell(c, sh, o.ProjectDir) }
		}
		res := run(command, sk.Shell)
		if res.Err != nil || (res.ExitCode != 0 && !(res.ExitCode == 1 && exitOneIsResult(command, sk.Shell))) {
			msg := strings.TrimSpace(res.Output)
			why := fmt.Sprintf("exit code %d", res.ExitCode)
			if res.Err != nil {
				why = res.Err.Error()
			}
			if msg != "" {
				why += "\n" + msg
			}
			return "", fmt.Errorf("Shell command failed for pattern %q: %s", sg.pattern, why)
		}
		body.WriteString(capOutput(strings.TrimRight(res.Output, "\r\n")))
	}
	out := body.String()
	if args != "" && !used {
		out += "\n\nARGUMENTS: " + args
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<skill name=%q dir=%q>\n%s\n", sk.Name, sk.Dir, strings.TrimSpace(out))
	if att := attachFiles(refs, o.ProjectDir, sk.Dir); att != "" {
		b.WriteString("\nFiles referenced by the skill:\n" + att)
	}
	if files := supportingFiles(sk.Dir); len(files) > 0 {
		b.WriteString("\nSupporting files in the skill folder (open them with your tools when the instructions need them):\n")
		for _, f := range files {
			b.WriteString("- " + filepath.Join(sk.Dir, f) + "\n")
		}
	}
	b.WriteString("</skill>")
	return b.String(), nil
}

// argToken matches the argument placeholders with the backslashes before
// them: $ARGUMENTS[N], $ARGUMENTS, $N and $name.
var argToken = regexp.MustCompile(`(\\*)\$(?:ARGUMENTS\[(\d+)\]|(ARGUMENTS)|(\d+)|([A-Za-z_][A-Za-z0-9_]*))`)

// substituteArgs replaces the argument placeholders in one pass, so values
// that contain placeholders are inserted literally. A single backslash
// before a placeholder escapes it.
func substituteArgs(s, args string, parts, names []string, used *bool) string {
	return argToken.ReplaceAllStringFunc(s, func(m string) string {
		g := argToken.FindStringSubmatch(m)
		bs, token := g[1], m[len(g[1]):]
		value, known := "", false
		switch {
		case g[2] != "" || g[4] != "":
			idx := g[2]
			if idx == "" {
				idx = g[4]
			}
			known = true
			n, err := strconv.Atoi(idx)
			if err != nil || n >= len(parts) {
				if len(bs) == 1 {
					return token
				}
				return m // no argument there: left as written
			}
			value = parts[n]
		case g[3] != "":
			known, value = true, args
		default:
			for i, nm := range names {
				if nm == g[5] {
					known = true
					if i < len(parts) {
						value = parts[i]
					}
				}
			}
		}
		if !known {
			return m
		}
		if len(bs) == 1 {
			return token // escaped: a literal $...
		}
		*used = true
		return bs + value
	})
}

func substituteVars(s string, sk Skill, o RenderOptions) string {
	return strings.NewReplacer(
		"${OPENUAI_SKILL_DIR}", sk.Dir,
		"${OPENUAI_PROJECT_DIR}", o.ProjectDir,
		"${OPENUAI_SESSION_ID}", o.SessionID,
		"${OPENUAI_EFFORT}", o.Effort,
	).Replace(s)
}

// segment is a piece of the skill body: text, or an injected command.
type segment_ struct {
	text    string
	cmd     string
	pattern string // as written, for error messages
}

var inlineCmd = regexp.MustCompile("(^|[ \t\n])!`([^`\n]+)`")

// segment splits a body into text and commands: ```! fenced blocks and
// inline !`command` placed at the start of a line or after whitespace.
func segment(body string) []segment_ {
	var out []segment_
	var text strings.Builder
	flushText := func() {
		if text.Len() == 0 {
			return
		}
		t := text.String()
		text.Reset()
		last := 0
		for _, m := range inlineCmd.FindAllStringSubmatchIndex(t, -1) {
			// m[2]:m[3] is the leading whitespace, m[4]:m[5] the command.
			out = append(out, segment_{text: t[last:m[3]]})
			out = append(out, segment_{cmd: t[m[4]:m[5]], pattern: t[m[3]:m[1]]})
			last = m[1]
		}
		out = append(out, segment_{text: t[last:]})
	}
	lines := strings.SplitAfter(body, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r\n") == "```!" {
			var cmd []string
			j := i + 1
			for ; j < len(lines) && strings.TrimRight(lines[j], " \t\r\n") != "```"; j++ {
				cmd = append(cmd, strings.TrimRight(lines[j], "\r\n"))
			}
			if j < len(lines) {
				flushText()
				c := strings.Join(cmd, "\n")
				out = append(out, segment_{cmd: c, pattern: "```!\n" + c + "\n```"})
				if strings.HasSuffix(lines[j], "\n") {
					text.WriteString("\n")
				}
				i = j
				continue
			}
		}
		text.WriteString(lines[i])
	}
	flushText()
	return out
}

func hasCommands(segs []segment_) bool {
	for _, s := range segs {
		if s.cmd != "" {
			return true
		}
	}
	return false
}

// Commands returns the injected commands of a skill body, as written.
func Commands(body string) []string {
	var out []string
	for _, s := range segment(body) {
		if s.cmd != "" {
			out = append(out, s.cmd)
		}
	}
	return out
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

// maxInjectedOutput is the inline ceiling for an injected command's output;
// past it the output is saved to a file and a preview is injected.
const maxInjectedOutput = 30000

func capOutput(s string) string {
	if len(s) <= maxInjectedOutput {
		return s
	}
	f, err := os.CreateTemp("", "openuai-skill-output-*.txt")
	if err != nil {
		return s[:maxInjectedOutput] + "\n... (output truncated)"
	}
	defer f.Close()
	_, _ = f.WriteString(s)
	return fmt.Sprintf("[Output too large (%d bytes): saved to %s. Preview:]\n%s\n...", len(s), f.Name(), s[:2000])
}

// fileRef finds @path references written at the start of a line or after
// whitespace.
var fileRef = regexp.MustCompile(`(?:^|[ \t\n(])@([^\s` + "`" + `'"<>()]+)`)

func fileRefs(text string) []string {
	var out []string
	for _, m := range fileRef.FindAllStringSubmatch(text, -1) {
		if p := strings.TrimRight(m[1], ".,;:!?"); p != "" {
			out = append(out, p)
		}
	}
	return out
}

const maxAttachedFile = 256 << 10

// attachFiles returns the contents of the referenced files that exist:
// absolute paths, or relative to the project and then to the skill folder.
func attachFiles(refs []string, projectDir, skillDir string) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, r := range refs {
		var cands []string
		if filepath.IsAbs(r) {
			cands = []string{r}
		} else {
			if projectDir != "" {
				cands = append(cands, filepath.Join(projectDir, r))
			}
			cands = append(cands, filepath.Join(skillDir, r))
		}
		for _, p := range cands {
			st, err := os.Stat(p)
			if err != nil || !st.Mode().IsRegular() || st.Size() > maxAttachedFile || seen[p] {
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil || strings.ContainsRune(string(data), 0) {
				continue
			}
			seen[p] = true
			fmt.Fprintf(&b, "<file path=%q>\n%s\n</file>\n", p, strings.TrimRight(string(data), "\n"))
			break
		}
	}
	return b.String()
}

func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quote := rune(0)
	in := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, in = r, true
		case quote == 0 && (r == ' ' || r == '\t' || r == '\n'):
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

func supportingFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || len(out) >= 50 {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel != "SKILL.md" {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// ErrNoBash is returned when a skill that sets shell: bash has commands to
// inject and bash is not installed.
var ErrNoBash = errors.New("bash not found")
