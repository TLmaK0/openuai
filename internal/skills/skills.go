// Package skills loads skills: named packages of instructions (plus optional
// supporting files) that the assistant loads only when a task needs them.
// The format is Claude Code's (a folder with a SKILL.md and YAML
// frontmatter), in OpenUAI's own folders:
//
//	<config dir>/skills/<name>/SKILL.md     user (all projects)
//	<project>/.openuai/skills/<name>/SKILL.md  project
//
// When both define the same name, the user's skill wins (as in Claude Code).
package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is one loaded skill.
type Skill struct {
	Name                   string `json:"name"`
	Description            string `json:"description"`
	WhenToUse              string `json:"when_to_use,omitempty"`
	ArgumentHint           string `json:"argument_hint,omitempty"`
	DisableModelInvocation bool   `json:"disable_model_invocation,omitempty"`
	UserInvocable          bool   `json:"user_invocable"`
	Dir                    string `json:"dir"`
	Source                 string `json:"source"` // user or project
	Body                   string `json:"-"`
}

// Set is the skills found, by name, with problems found while loading.
type Set struct {
	Skills   []Skill  `json:"skills"`
	Warnings []string `json:"warnings,omitempty"`
}

// Load reads the user and project skills. Called on every use, so edits to a
// skill apply without restarting.
func Load(configDir, workDir string) *Set {
	s := &Set{Skills: []Skill{}}
	seen := map[string]bool{}
	if configDir != "" {
		s.loadDir(filepath.Join(configDir, "skills"), "user", seen)
	}
	if workDir != "" {
		s.loadDir(filepath.Join(workDir, ".openuai", "skills"), "project", seen)
	}
	sort.Slice(s.Skills, func(i, j int) bool { return s.Skills[i].Name < s.Skills[j].Name })
	return s
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

func (s *Set) loadDir(dir, source string, seen map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		st, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil || !st.IsDir() {
			continue
		}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s skill %s: %v", source, e.Name(), err))
			continue
		}
		sk, warn := Parse(data, e.Name())
		if warn != "" {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s skill %s: %s", source, e.Name(), warn))
		}
		if !validName.MatchString(sk.Name) {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s skill %s: invalid name %q", source, e.Name(), sk.Name))
			continue
		}
		if seen[sk.Name] {
			continue // a user skill with the same name wins
		}
		seen[sk.Name] = true
		sk.Dir = filepath.Join(dir, e.Name())
		sk.Source = source
		s.Skills = append(s.Skills, sk)
	}
}

type frontmatter struct {
	Name                   string    `yaml:"name"`
	Description            string    `yaml:"description"`
	WhenToUse              string    `yaml:"when_to_use"`
	ArgumentHint           string    `yaml:"argument-hint"`
	DisableModelInvocation yamlBool  `yaml:"disable-model-invocation"`
	UserInvocable          *yamlBool `yaml:"user-invocable"`
}

// yamlBool accepts the forms Claude Code accepts: true/false, yes/no,
// on/off, 1/0, in any case.
type yamlBool bool

func (b *yamlBool) UnmarshalYAML(n *yaml.Node) error {
	switch strings.ToLower(strings.TrimSpace(n.Value)) {
	case "true", "yes", "on", "1":
		*b = true
	case "false", "no", "off", "0", "":
		*b = false
	default:
		return fmt.Errorf("not a boolean: %q", n.Value)
	}
	return nil
}

// Parse reads a SKILL.md. dirName is the default name. A frontmatter that
// does not parse leaves the skill with no fields set and a warning, as in
// Claude Code.
func Parse(data []byte, dirName string) (Skill, string) {
	sk := Skill{Name: dirName, UserInvocable: true}
	body := string(data)
	warn := ""
	if bytes.HasPrefix(data, []byte("---")) {
		rest := strings.TrimPrefix(strings.TrimPrefix(body, "---"), "\r")
		if strings.HasPrefix(rest, "\n") {
			if i := strings.Index(rest, "\n---"); i >= 0 {
				head := rest[1:i]
				body = strings.TrimLeft(strings.TrimPrefix(rest[i+4:], "\r"), "\n")
				var fm frontmatter
				if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
					warn = "frontmatter does not parse: " + err.Error()
				} else {
					if strings.TrimSpace(fm.Name) != "" {
						sk.Name = strings.TrimSpace(fm.Name)
					}
					sk.Description = strings.TrimSpace(fm.Description)
					sk.WhenToUse = strings.TrimSpace(fm.WhenToUse)
					sk.ArgumentHint = strings.TrimSpace(fm.ArgumentHint)
					sk.DisableModelInvocation = bool(fm.DisableModelInvocation)
					if fm.UserInvocable != nil {
						sk.UserInvocable = bool(*fm.UserInvocable)
					}
				}
			}
		}
	}
	sk.Body = strings.TrimSpace(body)
	if sk.Description == "" {
		for _, l := range strings.Split(sk.Body, "\n") {
			if l = strings.TrimSpace(strings.TrimLeft(l, "# ")); l != "" {
				sk.Description = l
				break
			}
		}
	}
	return sk, warn
}

// Get returns the skill called name.
func (s *Set) Get(name string) (Skill, bool) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	for _, sk := range s.Skills {
		if sk.Name == name {
			return sk, true
		}
	}
	return Skill{}, false
}

const maxListing = 1536

// Describe lists the skills the assistant may load on its own, with their
// descriptions, for the skill tool's description.
func (s *Set) Describe() string {
	var b strings.Builder
	for _, sk := range s.Skills {
		if sk.DisableModelInvocation {
			continue
		}
		d := sk.Description
		if sk.WhenToUse != "" {
			d += " " + sk.WhenToUse
		}
		if len(d) > maxListing {
			d = d[:maxListing] + "…"
		}
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, strings.ReplaceAll(d, "\n", " "))
	}
	return b.String()
}

var (
	argIndex = regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]|\$(\d+)`)
)

// Render returns the skill's instructions ready for the model: variables
// replaced (${OPENUAI_SKILL_DIR}, ${OPENUAI_PROJECT_DIR}, $ARGUMENTS, $N,
// $ARGUMENTS[N]) and its supporting files listed.
func Render(sk Skill, args, projectDir string) string {
	body := strings.ReplaceAll(sk.Body, "${OPENUAI_SKILL_DIR}", sk.Dir)
	body = strings.ReplaceAll(body, "${OPENUAI_PROJECT_DIR}", projectDir)
	parts := splitArgs(args)
	used := false
	body = argIndex.ReplaceAllStringFunc(body, func(m string) string {
		sub := argIndex.FindStringSubmatch(m)
		idx := sub[1]
		if idx == "" {
			idx = sub[2]
		}
		n, _ := strconv.Atoi(idx)
		used = true
		if n < len(parts) {
			return parts[n]
		}
		return m
	})
	if strings.Contains(body, "$ARGUMENTS") {
		body = strings.ReplaceAll(body, "$ARGUMENTS", args)
		used = true
	}
	if args != "" && !used {
		body += "\n\nARGUMENTS: " + args
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<skill name=%q dir=%q>\n%s\n", sk.Name, sk.Dir, body)
	if files := supportingFiles(sk.Dir); len(files) > 0 {
		b.WriteString("\nSupporting files in the skill folder (open them with your tools when the instructions need them):\n")
		for _, f := range files {
			b.WriteString("- " + filepath.Join(sk.Dir, f) + "\n")
		}
	}
	b.WriteString("</skill>")
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

// ParseInvocation recognises a message that starts with /<skill> [args].
func ParseInvocation(msg string) (name, args string, ok bool) {
	msg = strings.TrimSpace(msg)
	if !strings.HasPrefix(msg, "/") {
		return "", "", false
	}
	fields := strings.SplitN(msg[1:], " ", 2)
	name = strings.TrimSpace(fields[0])
	if i := strings.IndexAny(name, "\n\t"); i >= 0 {
		args = strings.TrimSpace(name[i:])
		name = name[:i]
	}
	if !validName.MatchString(name) {
		return "", "", false
	}
	if len(fields) > 1 {
		args = strings.TrimSpace(strings.TrimSpace(args + " " + fields[1]))
	}
	return name, args, true
}
