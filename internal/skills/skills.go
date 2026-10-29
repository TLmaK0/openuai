// Package skills loads skills: named packages of instructions (plus optional
// supporting files) that the assistant loads only when a task needs them.
// The format is Claude Code's (a folder with a SKILL.md and YAML
// frontmatter), in OpenUAI's own folders:
//
//	<config dir>/skills/<name>/SKILL.md             user (all projects)
//	<project>/.openuai/skills/<name>/SKILL.md       project (and its parents up to the repository root)
//	<project>/<sub>/.openuai/skills/<name>/SKILL.md nested: loaded once the assistant works on files in <sub>
//
// When the user and the project define the same name, the user's skill wins
// (as in Claude Code). A nested skill whose name is taken stays available
// under a directory-qualified name, <sub>:<name>.
package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Visibility values (skill_overrides in the config, Claude Code's skillOverrides).
const (
	VisibilityOn       = "on"
	VisibilityNameOnly = "name-only"
	VisibilityUserOnly = "user-invocable-only"
	VisibilityOff      = "off"
)

// Skill is one loaded skill.
type Skill struct {
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	WhenToUse              string   `json:"when_to_use,omitempty"`
	ArgumentHint           string   `json:"argument_hint,omitempty"`
	Arguments              []string `json:"arguments,omitempty"`
	DisableModelInvocation bool     `json:"disable_model_invocation,omitempty"`
	UserInvocable          bool     `json:"user_invocable"`
	AllowedTools           []string `json:"allowed_tools,omitempty"`
	DisallowedTools        []string `json:"disallowed_tools,omitempty"`
	Model                  string   `json:"model,omitempty"`
	Effort                 string   `json:"effort,omitempty"`
	Context                string   `json:"context,omitempty"` // "fork" runs it in a sub-agent
	Agent                  string   `json:"agent,omitempty"`   // sub-agent type for context: fork
	Background             bool     `json:"background"`        // context: fork runs in the background (default true)
	Paths                  []string `json:"paths,omitempty"`
	Shell                  string   `json:"shell,omitempty"` // bash (default) or powershell
	// Hooks is the frontmatter's hooks configuration, kept as written. It is
	// not active yet.
	Hooks         map[string]any `json:"hooks,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	License       string         `json:"license,omitempty"`
	Compatibility string         `json:"compatibility,omitempty"`

	Dir     string `json:"dir"`
	DirName string `json:"dir_name"`
	Source  string `json:"source"` // user, project or nested
	// Scope is the subdirectory (relative to the project, with /) of a nested skill.
	Scope string `json:"scope,omitempty"`
	// Visibility comes from skill_overrides ("on" when absent).
	Visibility string `json:"visibility"`
	Body       string `json:"-"`
}

// Forked reports whether the skill runs in a sub-agent (context: fork).
func (sk Skill) Forked() bool { return sk.Context == "fork" }

// ModelInvocable reports whether the assistant may load the skill on its own.
func (sk Skill) ModelInvocable() bool {
	return !sk.DisableModelInvocation && sk.Visibility != VisibilityUserOnly && sk.Visibility != VisibilityOff
}

// UserCanInvoke reports whether the user may run the skill with /name.
func (sk Skill) UserCanInvoke() bool {
	return sk.UserInvocable && sk.Visibility != VisibilityOff
}

// Set is the skills found, by name, with problems found while loading.
type Set struct {
	Skills   []Skill  `json:"skills"`
	Warnings []string `json:"warnings,omitempty"`
}

// Options says where to look for skills.
type Options struct {
	ConfigDir string
	WorkDir   string
	// NestedDirs are directories below WorkDir whose .openuai/skills were
	// activated (the assistant worked on files there).
	NestedDirs []string
	// Overrides maps skill names to a visibility (skill_overrides).
	Overrides map[string]string
}

// Load reads the user and project skills. Called on every use, so edits to a
// skill apply without restarting.
func Load(configDir, workDir string) *Set {
	return LoadWith(Options{ConfigDir: configDir, WorkDir: workDir})
}

// LoadWith reads the skills described by o.
func LoadWith(o Options) *Set {
	s := &Set{Skills: []Skill{}}
	l := loader{set: s, seen: map[string]bool{}, real: map[string]bool{}}
	if o.ConfigDir != "" {
		l.loadDir(filepath.Join(o.ConfigDir, "skills"), "user", "")
	}
	if o.WorkDir != "" {
		for _, d := range ProjectSkillDirs(o.WorkDir) {
			l.loadDir(d, "project", "")
		}
		nested := append([]string(nil), o.NestedDirs...)
		sort.Strings(nested)
		for _, d := range nested {
			rel, err := filepath.Rel(o.WorkDir, d)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}
			l.loadDir(filepath.Join(d, ".openuai", "skills"), "nested", filepath.ToSlash(rel))
		}
	}
	for i := range s.Skills {
		s.Skills[i].Visibility = VisibilityOn
		for _, key := range []string{s.Skills[i].Name, s.Skills[i].DirName} {
			if v, ok := o.Overrides[key]; ok {
				switch v {
				case VisibilityOn, VisibilityNameOnly, VisibilityUserOnly, VisibilityOff:
					s.Skills[i].Visibility = v
				default:
					s.Warnings = append(s.Warnings, fmt.Sprintf("skill_overrides: invalid value %q for %s", v, key))
				}
				break
			}
		}
	}
	sort.Slice(s.Skills, func(i, j int) bool { return s.Skills[i].Name < s.Skills[j].Name })
	return s
}

// ProjectSkillDirs lists the project skill folders for workDir: its own
// .openuai/skills and those of its parents up to the repository root
// (closest first). In a linked git worktree whose root has no
// .openuai/skills, the main checkout's are used instead.
func ProjectSkillDirs(workDir string) []string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	root := ""
	for d := abs; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			root = d
			break
		}
		p := filepath.Dir(d)
		if p == d {
			break
		}
		d = p
	}
	if root == "" {
		return []string{filepath.Join(abs, ".openuai", "skills")}
	}
	var out []string
	for d := abs; ; d = filepath.Dir(d) {
		out = append(out, filepath.Join(d, ".openuai", "skills"))
		if d == root || filepath.Dir(d) == d {
			break
		}
	}
	if st, err := os.Stat(filepath.Join(root, ".openuai", "skills")); err != nil || !st.IsDir() {
		if main := mainCheckout(root); main != "" && main != root {
			out = append(out, filepath.Join(main, ".openuai", "skills"))
		}
	}
	return out
}

// mainCheckout returns the main checkout of a linked worktree rooted at root
// (whose .git is a file "gitdir: <main>/.git/worktrees/<name>").
func mainCheckout(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(root, gitdir)
	}
	// <main>/.git/worktrees/<name>
	if filepath.Base(filepath.Dir(gitdir)) != "worktrees" {
		return ""
	}
	dotGit := filepath.Dir(filepath.Dir(gitdir))
	if filepath.Base(dotGit) != ".git" {
		return ""
	}
	return filepath.Dir(dotGit)
}

var (
	validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
	// invocationName also accepts the directory-qualified name of a nested
	// skill: apps/web:deploy.
	invocationName = regexp.MustCompile(`^(?:[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*:)?[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
)

// reservedName reports names Claude Code reserves (they never load).
func reservedName(n string) bool {
	l := strings.ToLower(n)
	return l == "synced" || l == "anthropic-skills" || strings.HasPrefix(l, "anthropic-skills:")
}

type loader struct {
	set  *Set
	seen map[string]bool // names taken
	real map[string]bool // real paths loaded (symlinked folders load once)
}

func (l *loader) loadDir(dir, source, scope string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	s := l.set
	for _, e := range entries {
		skillDir := filepath.Join(dir, e.Name())
		st, err := os.Stat(skillDir)
		if err != nil || !st.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if reservedName(e.Name()) {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s skill %s: the name is reserved", source, e.Name()))
			continue
		}
		path := filepath.Join(skillDir, "SKILL.md")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s skill %s: %v", source, e.Name(), err))
			continue
		}
		if r, err := filepath.EvalSymlinks(skillDir); err == nil {
			if l.real[r] {
				continue
			}
			l.real[r] = true
		}
		sk, warn := Parse(data, e.Name())
		if warn != "" {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s skill %s: %s", source, e.Name(), warn))
		}
		if !validName.MatchString(sk.Name) || reservedName(sk.Name) {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s skill %s: invalid name %q", source, e.Name(), sk.Name))
			continue
		}
		sk.Dir = skillDir
		sk.DirName = e.Name()
		sk.Source = source
		sk.Scope = scope
		if l.seen[sk.Name] {
			if scope == "" {
				continue // the user's (or the closest project) skill wins
			}
			// A nested skill keeps a directory-qualified name.
			sk.Name = scope + ":" + sk.Name
			if l.seen[sk.Name] {
				continue
			}
		}
		if scope != "" {
			sk.Description = strings.TrimSpace(sk.Description + " (applies to files in " + scope + "/)")
		}
		l.seen[sk.Name] = true
		s.Skills = append(s.Skills, sk)
	}
}

// fmList is a frontmatter field that takes a string or a YAML list.
type fmList struct {
	scalar string
	items  []string
	set    bool
}

func (f *fmList) UnmarshalYAML(n *yaml.Node) error {
	f.set = true
	switch n.Kind {
	case yaml.ScalarNode:
		f.scalar = n.Value
	case yaml.SequenceNode:
		f.items = []string{}
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("list items must be strings")
			}
			if v := strings.TrimSpace(c.Value); v != "" {
				f.items = append(f.items, v)
			}
		}
	default:
		return fmt.Errorf("expected a string or a list")
	}
	return nil
}

// split returns the items: the list as is, or the string split by split.
func (f fmList) split(split func(string) []string) []string {
	if !f.set {
		return nil
	}
	if f.items != nil {
		return f.items
	}
	return split(f.scalar)
}

type frontmatter struct {
	Name                   string    `yaml:"name"`
	Description            string    `yaml:"description"`
	WhenToUse              string    `yaml:"when_to_use"`
	ArgumentHint           string    `yaml:"argument-hint"`
	Arguments              fmList    `yaml:"arguments"`
	DisableModelInvocation yamlBool  `yaml:"disable-model-invocation"`
	UserInvocable          *yamlBool `yaml:"user-invocable"`
	AllowedTools           fmList    `yaml:"allowed-tools"`
	DisallowedTools        fmList    `yaml:"disallowed-tools"`
	Model                  string    `yaml:"model"`
	Effort                 string    `yaml:"effort"`
	Context                string    `yaml:"context"`
	Agent                  string    `yaml:"agent"`
	Background             *yamlBool `yaml:"background"`
	Paths                  fmList    `yaml:"paths"`
	Shell                  string    `yaml:"shell"`
	Hooks                  yaml.Node `yaml:"hooks"`
	Metadata               yaml.Node `yaml:"metadata"`
	License                string    `yaml:"license"`
	Compatibility          string    `yaml:"compatibility"`
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
	sk := Skill{Name: dirName, DirName: dirName, UserInvocable: true, Background: true, Visibility: VisibilityOn}
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
					warn = applyFrontmatter(&sk, fm)
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

var argName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func applyFrontmatter(sk *Skill, fm frontmatter) string {
	var warns []string
	if strings.TrimSpace(fm.Name) != "" {
		sk.Name = strings.TrimSpace(fm.Name)
	}
	sk.Description = strings.TrimSpace(fm.Description)
	sk.WhenToUse = strings.TrimSpace(fm.WhenToUse)
	sk.ArgumentHint = strings.TrimSpace(fm.ArgumentHint)
	for _, a := range fm.Arguments.split(strings.Fields) {
		if argName.MatchString(a) && a != "ARGUMENTS" {
			sk.Arguments = append(sk.Arguments, a)
		} else {
			warns = append(warns, fmt.Sprintf("ignored argument name %q", a))
		}
	}
	sk.DisableModelInvocation = bool(fm.DisableModelInvocation)
	if fm.UserInvocable != nil {
		sk.UserInvocable = bool(*fm.UserInvocable)
	}
	sk.AllowedTools = fm.AllowedTools.split(SplitToolList)
	sk.DisallowedTools = fm.DisallowedTools.split(SplitToolList)
	sk.Model = strings.TrimSpace(fm.Model)
	if e := strings.ToLower(strings.TrimSpace(fm.Effort)); e != "" {
		switch e {
		case "low", "medium", "high", "xhigh", "max":
			sk.Effort = e
		default:
			warns = append(warns, fmt.Sprintf("ignored effort %q", fm.Effort))
		}
	}
	if c := strings.ToLower(strings.TrimSpace(fm.Context)); c != "" {
		if c == "fork" {
			sk.Context = c
		} else {
			warns = append(warns, fmt.Sprintf("ignored context %q", fm.Context))
		}
	}
	sk.Agent = strings.TrimSpace(fm.Agent)
	if fm.Background != nil {
		sk.Background = bool(*fm.Background)
	}
	sk.Paths = fm.Paths.split(func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	})
	if sh := strings.ToLower(strings.TrimSpace(fm.Shell)); sh != "" {
		if sh == "bash" || sh == "powershell" {
			sk.Shell = sh
		} else {
			warns = append(warns, fmt.Sprintf("ignored shell %q (bash or powershell)", fm.Shell))
		}
	}
	if fm.Hooks.Kind == yaml.MappingNode {
		var h map[string]any
		if err := fm.Hooks.Decode(&h); err == nil {
			sk.Hooks = h
		}
	}
	if fm.Metadata.Kind == yaml.MappingNode {
		var m map[string]any
		if err := fm.Metadata.Decode(&m); err == nil {
			sk.Metadata = m
		}
	}
	sk.License = strings.TrimSpace(fm.License)
	sk.Compatibility = strings.TrimSpace(fm.Compatibility)
	if len(sk.Compatibility) > 500 {
		sk.Compatibility = sk.Compatibility[:500]
	}
	return strings.Join(warns, "; ")
}

// SplitToolList splits an allowed-tools / disallowed-tools string on spaces
// and commas outside parentheses: "Bash(git add *) Read, Grep".
func SplitToolList(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case r == '(':
			depth++
			cur.WriteRune(r)
		case r == ')':
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		case depth == 0 && (r == ' ' || r == ',' || r == '\t' || r == '\n'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// Get returns the skill called name: by its name, or else by its folder name.
func (s *Set) Get(name string) (Skill, bool) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	for _, sk := range s.Skills {
		if sk.Name == name {
			return sk, true
		}
	}
	for _, sk := range s.Skills {
		if sk.DirName == name && sk.Scope == "" {
			return sk, true
		}
	}
	return Skill{}, false
}

// ParseInvocation recognises a message that starts with /<skill> [args].
func ParseInvocation(msg string) (name, args string, ok bool) {
	msg = strings.TrimSpace(msg)
	if !strings.HasPrefix(msg, "/") {
		return "", "", false
	}
	end := strings.IndexAny(msg, " \t\r\n")
	if end < 0 {
		end = len(msg)
	}
	name = msg[1:end]
	if !invocationName.MatchString(name) {
		return "", "", false
	}
	return name, strings.TrimSpace(msg[end:]), true
}

// Mentions returns the names written as /name, as separate words with no
// punctuation attached, after the start of msg. Writing a skill's name that
// way gives the assistant permission to run it while it answers.
func Mentions(msg string) []string {
	var out []string
	for i, w := range strings.Fields(msg) {
		if i == 0 || !strings.HasPrefix(w, "/") {
			continue
		}
		if n := w[1:]; invocationName.MatchString(n) {
			out = append(out, n)
		}
	}
	return out
}
