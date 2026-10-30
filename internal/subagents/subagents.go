// Package subagents loads declarative subagent definitions (Markdown files
// with a YAML frontmatter) and turns them into the settings a subagent runs
// with: its prompt, its tools, its model and its turn limit.
//
// Definitions live in .openuai/agents/ of the project (every directory from
// the repository root down to the working directory; the closest one wins)
// and in the user's agents directory. Project definitions win over user ones.
package subagents

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"openuai/internal/tools"

	"openuai/internal/llm"

	"openuai/internal/hooks"
)

const (
	ScopeProject = "project"
	ScopeUser    = "user"
)

// projectAgentsDir is where a project keeps its definitions, relative to
// each directory between the repository root and the working directory.
var projectAgentsDir = filepath.Join(".openuai", "agents")

// DefaultPrompt is used when a definition has no body, or no definition is
// named at all.
const DefaultPrompt = `You are a focused sub-agent of OpenUAI working on a specific task.
Complete it efficiently using your tools. Be direct and concise.
When you finish, reply with a clear summary of what you found or did: it is all the main agent will see.`

const maxInstructionBytes = 64 * 1024

// AlwaysExcluded are tools no subagent gets: they change what the main
// conversation listens to. spawn_agents is not here: sub-agents may delegate
// up to agent.MaxSubAgentDepth levels; the deepest level does not get it.
var AlwaysExcluded = []string{"watch_chat", "unwatch_chat"}

// Definition is one parsed subagent definition.
type Definition struct {
	Name            string
	Description     string
	Tools           []string // allowed tools (names or patterns); only meaningful when HasTools
	HasTools        bool     // false: every tool available to subagents
	DisallowedTools []string
	Model           string
	MaxTurns        int
	OmitAgentsMd    bool
	Isolation       string          // "worktree": run in a temporary git worktree
	MCPServers      []MCPServerSpec // mcpServers: references and inline servers
	Memory          string          // persistent memory scope: user, project, local; empty: none
	Effort          string          // low, medium, high, xhigh, max; empty: the session default
	Color           string          // display colour name (see Colors); empty: automatic
	PermissionMode  string          // "", acceptEdits, dontAsk or plan
	Background      bool            // always run detached from the turn that launches it
	Hooks           hooks.Config    // hooks active only while this sub-agent runs
	Skills          []string        // skills preloaded into the sub-agent\'s prompt
	// InitialPrompt is sent as the first user message when the agent runs
	// as the main conversation (ignored for sub-agents).
	InitialPrompt string
	// MainHooks are the hooks as written (Stop stays Stop), used when the
	// agent runs as the main conversation.
	MainHooks hooks.Config
	Prompt    string
	Path      string
	Scope     string
}

// StringList accepts both a YAML list and a comma-separated string.
type StringList []string

func (l *StringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*l = splitList(n.Value)
		return nil
	case yaml.SequenceNode:
		out := StringList{}
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: expected a tool name", c.Line)
			}
			out = append(out, splitList(c.Value)...)
		}
		*l = out
		return nil
	}
	return fmt.Errorf("line %d: expected a list or a comma-separated string", n.Line)
}

// splitList splits on commas outside parentheses, so "Agent(a, b), Read"
// is two entries.
func splitList(s string) []string {
	out := []string{}
	depth, start := 0, 0
	add := func(p string) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				add(s[start:i])
				start = i + 1
			}
		}
	}
	add(s[start:])
	return out
}

// frontmatter holds the supported fields. Unknown fields are ignored.
type frontmatter struct {
	Name            string       `yaml:"name"`
	Description     string       `yaml:"description"`
	Tools           *StringList  `yaml:"tools"`
	DisallowedTools StringList   `yaml:"disallowedTools"`
	Model           string       `yaml:"model"`
	MaxTurns        int          `yaml:"maxTurns"`
	OmitAgentsMd    bool         `yaml:"omitAgentsMd"`
	Isolation       string       `yaml:"isolation"`
	MCPServers      []yaml.Node  `yaml:"mcpServers"`
	Memory          string       `yaml:"memory"`
	Effort          string       `yaml:"effort"`
	Color           string       `yaml:"color"`
	PermissionMode  string       `yaml:"permissionMode"`
	Background      bool         `yaml:"background"`
	Hooks           hooks.Config `yaml:"hooks"`
	Skills          StringList   `yaml:"skills"`
	InitialPrompt   string       `yaml:"initialPrompt"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// errNoName marks files that are not definitions (no frontmatter or no
// name): they are documentation and are skipped without a warning.
var errNoName = errors.New("not a definition: no name")

// Parse reads one definition file.
func Parse(data []byte) (*Definition, error) {
	text := strings.TrimPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, errNoName
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, errors.New("frontmatter is not closed with ---")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fm); err != nil {
		return nil, fmt.Errorf("invalid frontmatter: %w", err)
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		return nil, errNoName
	}
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid name %q: use lowercase letters, digits, '-' and '_'", name)
	}
	if fm.MaxTurns < 0 {
		return nil, errors.New("maxTurns must be a positive number")
	}
	defHooks, err := parseHooks(fm.Hooks, true)
	if err != nil {
		return nil, err
	}
	mainHooks, _ := parseHooks(fm.Hooks, false)
	isolation := strings.ToLower(strings.TrimSpace(fm.Isolation))
	if isolation != "" && isolation != IsolationWorktree {
		return nil, fmt.Errorf("invalid isolation %q: the only value is worktree", fm.Isolation)
	}
	mcpServers, err := parseMCPServers(fm.MCPServers)
	if err != nil {
		return nil, err
	}
	mem := strings.ToLower(strings.TrimSpace(fm.Memory))
	if mem != "" && mem != MemoryUser && mem != MemoryProject && mem != MemoryLocal {
		return nil, fmt.Errorf("invalid memory %q: use user, project or local", fm.Memory)
	}
	effort := strings.ToLower(strings.TrimSpace(fm.Effort))
	if effort != "" && !llm.ValidEffort(effort) {
		return nil, fmt.Errorf("invalid effort %q: use one of %s", fm.Effort, strings.Join(llm.Efforts, ", "))
	}
	color := strings.ToLower(strings.TrimSpace(fm.Color))
	if color != "" && !validColor(color) {
		return nil, fmt.Errorf("invalid color %q: use one of %s", fm.Color, strings.Join(Colors, ", "))
	}
	permMode, okMode := permissionModes[strings.ToLower(strings.TrimSpace(fm.PermissionMode))]
	if strings.TrimSpace(fm.PermissionMode) != "" && !okMode {
		return nil, fmt.Errorf("invalid permissionMode %q: use default, acceptEdits, auto, dontAsk, bypassPermissions or plan", fm.PermissionMode)
	}
	d := &Definition{
		Name:            name,
		Description:     strings.TrimSpace(fm.Description),
		DisallowedTools: []string(fm.DisallowedTools),
		Model:           strings.TrimSpace(fm.Model),
		MaxTurns:        fm.MaxTurns,
		OmitAgentsMd:    fm.OmitAgentsMd,
		Isolation:       isolation,
		MCPServers:      mcpServers,
		Memory:          mem,
		Effort:          effort,
		Color:           color,
		PermissionMode:  permMode,
		Background:      fm.Background,
		Hooks:           defHooks,
		Skills:          []string(fm.Skills),
		InitialPrompt:   strings.TrimSpace(fm.InitialPrompt),
		MainHooks:       mainHooks,
		Prompt:          strings.TrimSpace(strings.Join(lines[end+1:], "\n")),
	}
	if fm.Tools != nil {
		d.HasTools = true
		d.Tools = []string(*fm.Tools)
	}
	return d, nil
}

// Colors are the display colours a definition may choose (same names as
// Claude Code).
var Colors = []string{"red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"}

func validColor(c string) bool {
	for _, k := range Colors {
		if c == k {
			return true
		}
	}
	return false
}

// Color returns the display colour of agent name, or "" for automatic.
func (s Set) Color(name string) string {
	if d, ok := s.Defs[name]; ok {
		return d.Color
	}
	return ""
}

// permissionModes maps the accepted permissionMode values (Claude Code names,
// any case) to the mode the sub-agent runs in. OpenUAI's main conversation
// always runs in the default mode, so, as Claude Code does in that case,
// bypassPermissions keeps the default mode, and auto (not available here)
// falls back to it too.
var permissionModes = map[string]string{
	"": "", "default": "", "manual": "", "auto": "", "bypasspermissions": "",
	"acceptedits": "acceptEdits", "dontask": "dontAsk", "plan": "plan",
}

// Set is the result of loading every definition directory.
type Set struct {
	Defs     map[string]*Definition
	Warnings []string // definitions skipped because of an error
}

// Load starts from the built-in agents, then reads the user directory and
// then the project directories from the repository root down to workDir, so
// later (closer) ones win.
func Load(workDir, userDir string) Set {
	s := Set{Defs: map[string]*Definition{}}
	for _, d := range Builtins() {
		s.Defs[d.Name] = d
	}
	if userDir != "" {
		s.loadDir(userDir, ScopeUser)
	}
	for _, d := range projectChain(workDir) {
		s.loadDir(filepath.Join(d, projectAgentsDir), ScopeProject)
	}
	return s
}

func (s *Set) loadDir(dir, scope string) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return
	}
	seen := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %v", p, err))
			return nil
		}
		d, err := Parse(data)
		if errors.Is(err, errNoName) {
			return nil
		}
		if err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %v", p, err))
			return nil
		}
		if prev, dup := seen[d.Name]; dup {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s: agent %q is also defined in %s; using this one", p, d.Name, prev))
		}
		seen[d.Name] = p
		d.Path, d.Scope = p, scope
		s.Defs[d.Name] = d
		return nil
	})
}

// Names returns the defined agent names, sorted.
func (s Set) Names() []string {
	out := make([]string, 0, len(s.Defs))
	for n := range s.Defs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Describe lists the agents as "- name: description" lines.
func (s Set) Describe() string {
	var b strings.Builder
	for _, n := range s.Names() {
		desc := s.Defs[n].Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&b, "- %s: %s\n", n, desc)
	}
	return strings.TrimSpace(b.String())
}

// projectChain returns the directories from the repository root down to
// workDir. Outside a repository it is just workDir.
func projectChain(workDir string) []string {
	dir, err := filepath.Abs(workDir)
	if err != nil || workDir == "" {
		return nil
	}
	var chain []string
	d := dir
	for {
		chain = append(chain, d)
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			break
		}
		p := filepath.Dir(d)
		if p == d {
			chain = []string{dir}
			break
		}
		d = p
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func match(pattern, name string) bool {
	if ok, err := path.Match(pattern, name); err == nil {
		return ok
	}
	return pattern == name
}

// FilterTools returns the registry a subagent runs with. disallowedTools is
// applied first, then tools. With no definition the subagent gets every tool
// except AlwaysExcluded. It fails when no tool is left.
func FilterTools(reg *tools.Registry, d *Definition) (*tools.Registry, error) {
	return filterTools(reg, d, AlwaysExcluded)
}

func filterTools(reg *tools.Registry, d *Definition, exclude []string) (*tools.Registry, error) {
	allowed := map[string]bool{}
	for _, t := range reg.All() {
		n := t.Definition().Name
		excluded := false
		for _, x := range exclude {
			if n == x {
				excluded = true
			}
		}
		if !excluded {
			allowed[n] = true
		}
	}
	var unknown []string
	if d != nil {
		for _, p := range d.DisallowedTools {
			for n := range allowed {
				if match(p, n) {
					delete(allowed, n)
				}
			}
		}
		if d.HasTools {
			keep := map[string]bool{}
			for _, p := range d.Tools {
				p = toolPattern(p)
				hit := false
				for n := range allowed {
					if match(p, n) {
						keep[n] = true
						hit = true
					}
				}
				if !hit {
					unknown = append(unknown, p)
				}
			}
			allowed = keep
		}
	}
	if len(allowed) == 0 {
		name := "subagent"
		if d != nil {
			name = fmt.Sprintf("agent %q", d.Name)
		}
		msg := name + " has no tools left after applying tools/disallowedTools"
		if len(unknown) > 0 {
			msg += "; not recognized or not available: " + strings.Join(unknown, ", ")
		}
		return nil, errors.New(msg)
	}
	out := tools.NewRegistry()
	for _, t := range reg.All() {
		if allowed[t.Definition().Name] {
			out.Register(t)
		}
	}
	return out, nil
}

var families = []string{"opus", "sonnet", "haiku"}

func family(model string) string {
	l := strings.ToLower(model)
	for _, f := range families {
		if strings.Contains(l, f) {
			return f
		}
	}
	return ""
}

// ResolveModel picks the subagent's model: the one given when delegating,
// then the definition's, then the global setting, then the main agent's.
// "inherit" means the main agent's model, and a family alias that matches the
// main agent's family (e.g. "sonnet" while running a Sonnet model) resolves to
// exactly the main agent's model. A model that is not available falls back to
// the main agent's, with a notice explaining it.
func ResolveModel(call, def, global, parent string, available []string) (string, string) {
	want, fromGlobal := "", false
	if g := strings.TrimSpace(global); strings.EqualFold(g, "inherit") {
		global = "" // a global "inherit" is the same as no global default
	}
	for i, m := range []string{call, def, global} {
		if m = strings.TrimSpace(m); m != "" {
			want, fromGlobal = m, i == 2
			break
		}
	}
	if want == "" || strings.EqualFold(want, "inherit") || want == parent {
		return parent, ""
	}
	// Like Claude Code, a family alias from the call or the definition that
	// matches the main agent's family means exactly the main agent's model;
	// the global default always resolves to what it names.
	if f := family(parent); !fromGlobal && f != "" && strings.EqualFold(want, f) {
		return parent, ""
	}
	if len(available) == 0 {
		return want, ""
	}
	for _, a := range available {
		if a == want {
			return want, ""
		}
	}
	return parent, fmt.Sprintf("model %q is not available with the current provider; using the main agent's model %q", want, parent)
}

// ProjectInstructionFiles lists the AGENTS.md files ProjectInstructions
// loads for workDir.
func ProjectInstructionFiles(workDir string) []string {
	var out []string
	for _, d := range projectChain(workDir) {
		p := filepath.Join(d, "AGENTS.md")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// ProjectInstructions returns every AGENTS.md from the repository root down
// to workDir, in that order.
func ProjectInstructions(workDir string) string {
	var files []InstructionFile
	for _, d := range projectChain(workDir) {
		if f, ok := readInstructionFile(filepath.Join(d, "AGENTS.md"), ScopeProject); ok {
			files = append(files, f)
		}
	}
	return formatInstructionFiles(files)
}

// GitStatus is a short snapshot of the repository at workDir, or "" outside
// a repository.
func GitStatus(workDir string) string {
	run := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", workDir}, args...)...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	branch := run("rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" {
		return ""
	}
	status := run("status", "--short")
	if lines := strings.Split(status, "\n"); len(lines) > 40 {
		status = strings.Join(lines[:40], "\n") + fmt.Sprintf("\n... (%d more)", len(lines)-40)
	}
	if status == "" {
		status = "(clean)"
	}
	return fmt.Sprintf("Current branch: %s\n\nStatus:\n%s\n\nRecent commits:\n%s", branch, status, run("log", "--oneline", "-5"))
}

// SystemPrompt builds a subagent's system prompt: the definition's body (or
// the default prompt), the project's AGENTS.md files unless the definition
// sets omitAgentsMd, a git status snapshot and the working directory. The
// agent package appends the environment details.
func SystemPrompt(d *Definition, workDir string) string {
	p := DefaultPrompt
	if d != nil && d.Prompt != "" {
		p = d.Prompt
	}
	if d == nil || !d.OmitAgentsMd {
		if ins := ProjectInstructions(workDir); ins != "" {
			p += "\n\n## Project instructions (AGENTS.md)\n\n" + ins
		}
	}
	if gs := GitStatus(workDir); gs != "" {
		p += "\n\n## Git status (snapshot taken when this sub-agent started)\n\n" + gs
	}
	if workDir != "" {
		p += "\n\n## Working directory\n" + workDir
	}
	return p
}
