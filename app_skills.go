package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"openuai/internal/agent"
	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/skills"
	"openuai/internal/subagents"
	"openuai/internal/tools"
)

// skillSessionID is ${OPENUAI_SESSION_ID}: the same stable id hooks get as
// session_id, so a skill's output can be correlated with them.
const skillSessionID = "openuai"

// defaultSkillEffort is ${OPENUAI_EFFORT} when no effort level is set.
const defaultSkillEffort = "high"

// maxStackedSkills is how many skills one message can stack: the first
// plus up to five more (as Claude Code).
const maxStackedSkills = 6

// skillState is what the session knows about skills: the files the
// assistant worked on (for paths and nested skill folders), how often each
// skill was used (for the listing budget), the skills the user named in
// the current message, and the forked skills running in the background.
type skillState struct {
	mu         sync.Mutex
	touched    []string // project-relative paths, with /
	touchedSet map[string]bool
	nested     map[string]bool // directories whose .openuai/skills are active
	usage      map[string]int
	permitted  map[string]bool
	forks      map[string]int
	overLogged bool
}

const maxTouched = 5000

func (s *skillState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touched, s.touchedSet, s.nested, s.permitted = nil, nil, nil, nil
}

func (s *skillState) nestedDirs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.nested))
	for d := range s.nested {
		out = append(out, d)
	}
	return out
}

func (s *skillState) pathActive(sk skills.Skill) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.touched {
		if sk.MatchesPath(p) {
			return true
		}
	}
	return false
}

func (s *skillState) used(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usage == nil {
		s.usage = map[string]int{}
	}
	s.usage[name]++
}

func (s *skillState) usageCopy() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.usage))
	for k, v := range s.usage {
		out[k] = v
	}
	return out
}

func (s *skillState) setPermitted(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permitted = map[string]bool{}
	for _, n := range names {
		s.permitted[n] = true
	}
}

func (s *skillState) isPermitted(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permitted[name]
}

// forkStart records a background fork of name; it reports false when one
// is already running (the new invocation then waits for its result).
func (s *skillState) forkStart(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forks == nil {
		s.forks = map[string]int{}
	}
	if s.forks[name] > 0 {
		return false
	}
	s.forks[name]++
	return true
}

func (s *skillState) forkDone(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forks[name] > 0 {
		s.forks[name]--
	}
}

// touch records paths (project-relative) and nested skill folders; it
// reports whether anything new was recorded.
func (s *skillState) touch(rels, nestedDirs []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	if s.touchedSet == nil {
		s.touchedSet = map[string]bool{}
	}
	for _, r := range rels {
		if !s.touchedSet[r] && len(s.touched) < maxTouched {
			s.touchedSet[r] = true
			s.touched = append(s.touched, r)
			changed = true
		}
	}
	for _, d := range nestedDirs {
		if s.nested == nil {
			s.nested = map[string]bool{}
		}
		if !s.nested[d] {
			s.nested[d] = true
			changed = true
		}
	}
	return changed
}

// loadSkills reads the user, project and active nested skills. It runs on
// every use, so edits to a skill apply without restarting.
func (a *App) loadSkills() *skills.Set {
	o := skills.Options{WorkDir: a.GetWorkDir(), NestedDirs: a.skills.nestedDirs()}
	if a.cfg != nil {
		o.ConfigDir = a.cfg.ConfigDir()
		o.Overrides = a.cfg.SkillOverrides()
	}
	return skills.LoadWith(o)
}

// GetSkills lists the available skills (and problems loading them) for the UI.
func (a *App) GetSkills() *skills.Set {
	return a.loadSkills()
}

// skillListing is the skill tool's list of skills the assistant may load.
func (a *App) skillListing() string {
	o := skills.ListOptions{Usage: a.skills.usageCopy(), PathActive: a.skills.pathActive}
	if a.cfg != nil {
		o.Budget, o.MaxDescChars = a.cfg.SkillListingLimits()
	}
	list, over := a.loadSkills().Listing(o)
	a.skills.mu.Lock()
	logIt := over && !a.skills.overLogged
	a.skills.overLogged = over
	a.skills.mu.Unlock()
	if logIt {
		logger.Info("Skill listing over its budget: descriptions of the least used skills were dropped (skill_listing_budget_chars raises it)")
	}
	return list
}

// fileToolArgs are the arguments that name files, for the file tools.
var fileTools = map[string][]string{
	"read_file": {"path"}, "write_file": {"path"}, "list_dir": {"path"}, "delete_file": {"path"},
	"move_file": {"source", "destination"}, "search_files": {"path"},
}

// noteSkillPaths runs after each tool call of the main agent: it records the
// files the tool worked on and returns a note for the model when that made
// skills available (a skill's paths matched, or a nested .openuai/skills
// folder became active).
func (a *App) noteSkillPaths(tool string, args map[string]string) string {
	keys, ok := fileTools[tool]
	if !ok {
		return ""
	}
	work := a.GetWorkDir()
	if work == "" {
		return ""
	}
	var rels, nested []string
	for _, k := range keys {
		p := strings.TrimSpace(args[k])
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(work, p)
		}
		p = filepath.Clean(p)
		rel, err := filepath.Rel(work, p)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		rels = append(rels, filepath.ToSlash(rel))
		dir := p
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			dir = filepath.Dir(p)
		}
		for d := dir; d != work && strings.HasPrefix(d, work+string(filepath.Separator)); d = filepath.Dir(d) {
			if st, err := os.Stat(filepath.Join(d, ".openuai", "skills")); err == nil && st.IsDir() {
				nested = append(nested, d)
			}
		}
	}
	if len(rels) == 0 {
		return ""
	}
	before := map[string]bool{}
	for _, sk := range a.loadSkills().Skills {
		if sk.Listed(a.skills.pathActive) {
			before[sk.Name] = true
		}
	}
	if !a.skills.touch(rels, nested) {
		return ""
	}
	var lines []string
	for _, sk := range a.loadSkills().Skills {
		if sk.Listed(a.skills.pathActive) && !before[sk.Name] {
			logger.Info("Skill %s became available for %s", sk.Name, strings.Join(rels, ", "))
			lines = append(lines, "- "+sk.Name+": "+sk.Description)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "[Skills now available for the files you are working on]\n" + strings.Join(lines, "\n") + "\nLoad one with the skill tool when it applies to this work."
}

// renderOptions are the values a skill renders with. Injected commands run
// without asking only when the skill's allowed-tools or the user's
// permissions already allow bash; otherwise the invocation aborts.
func (a *App) renderOptions(sk skills.Skill, turn *tools.Turn) skills.RenderOptions {
	work := a.GetWorkDir()
	rules := skills.ToolRules(sk.AllowedTools, sk, work)
	effort := defaultSkillEffort
	if turn != nil && turn.Effort != "" {
		effort = turn.Effort
	}
	o := skills.RenderOptions{
		ProjectDir: work,
		SessionID:  skillSessionID,
		Effort:     effort,
		CanRun: func(cmd string) bool {
			if skills.AnyMatch(rules, "bash", cmd) {
				return true
			}
			if turn != nil && turn.Allowed != nil {
				return turn.Allowed("bash", cmd, "session")
			}
			return a.permissions != nil && a.permissions.Allowed("bash", "session")
		},
	}
	if a.cfg != nil {
		o.ShellDisabled = a.cfg.DisableSkillShellExecution()
	}
	return o
}

// skillTurnChange is what invoking sk changes for the rest of the turn.
func (a *App) skillTurnChange(ctx context.Context, sk skills.Skill) tools.TurnChange {
	work := a.GetWorkDir()
	c := tools.TurnChange{Skill: sk.Name, Effort: sk.Effort, Model: a.resolveSkillModel(ctx, sk)}
	if rules := skills.ToolRules(sk.AllowedTools, sk, work); len(rules) > 0 {
		c.Allow = func(tool, arg string) bool { return skills.AnyMatch(rules, tool, arg) }
	}
	c.Disallow = skills.ToolNames(skills.ToolRules(sk.DisallowedTools, sk, work))
	return c
}

// resolveSkillModel turns the skill's model into one the provider has; ""
// keeps the session's model (no model, inherit, or not available).
func (a *App) resolveSkillModel(ctx context.Context, sk skills.Skill) string {
	m := strings.TrimSpace(sk.Model)
	if m == "" || strings.EqualFold(m, "inherit") || a.cfg == nil {
		return ""
	}
	parent := a.cfg.DefaultModel()
	resolved, notice := subagents.ResolveModel(m, "", "", parent, llm.AvailableModels(ctx, a.activeProvider()))
	if notice != "" {
		logger.Info("Skill %s: %s", sk.Name, notice)
		return ""
	}
	if resolved == parent {
		return ""
	}
	return resolved
}

// invokeSkill runs the skill tool: the assistant loads a skill.
func (a *App) invokeSkill(ctx context.Context, name, args string) tools.Result {
	set := a.loadSkills()
	sk, ok := set.Get(name)
	if !ok {
		var names []string
		for _, s := range set.Skills {
			if s.ModelInvocable() {
				names = append(names, s.Name)
			}
		}
		return tools.Result{Error: fmt.Sprintf("no skill named %q (available: %s)", name, strings.Join(names, ", "))}
	}
	if sk.Visibility == skills.VisibilityOff {
		return tools.Result{Error: fmt.Sprintf("skill %q is turned off in the settings (skill_overrides)", sk.Name)}
	}
	if !sk.ModelInvocable() && !a.skills.isPermitted(sk.Name) {
		return tools.Result{Error: fmt.Sprintf("skill %q can only be run by the user (typing /%s); do not reproduce its steps another way", sk.Name, sk.Name)}
	}
	a.skills.used(sk.Name)
	turn := tools.TurnFrom(ctx)
	rendered, err := skills.Render(sk, args, a.renderOptions(sk, turn))
	if err != nil {
		logger.Error("Skill %s: %s", sk.Name, err.Error())
		return tools.Result{Error: err.Error()}
	}
	if sk.Forked() {
		logger.Info("Skill %s runs in a sub-agent (invoked by the assistant)", sk.Name)
		return a.runSkillFork(ctx, sk, rendered)
	}
	if turn != nil && turn.Apply != nil {
		turn.Apply(a.skillTurnChange(ctx, sk))
	}
	if turn != nil && turn.Has != nil && turn.Has(rendered) {
		return tools.Result{Output: fmt.Sprintf("Skill %s is already loaded in this conversation with the same content: keep following it.", sk.Name)}
	}
	logger.Info("Skill %s loaded by the assistant", sk.Name)
	return tools.Result{Output: rendered}
}

var forkIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// runSkillFork runs a skill with context: fork in a sub-agent of the skill's
// agent type: its rendered content is the task. It runs in the background
// (the result arrives later as a notification) unless the skill sets
// background: false or the same skill is already running in the background.
func (a *App) runSkillFork(ctx context.Context, sk skills.Skill, rendered string) tools.Result {
	c := a.skillTurnChange(ctx, sk)
	task := tools.SubTask{
		ID:          "skill-" + strings.Trim(forkIDUnsafe.ReplaceAllString(sk.Name, "-"), "-"),
		Description: "Carry out this skill's task. You do not see the conversation that invoked it.\n\n" + rendered,
		Agent:       a.forkAgentName(sk.Agent),
		Model:       sk.Model,
		Effort:      sk.Effort,
		Allow:       c.Allow,
		Disallow:    c.Disallow,
	}
	background := sk.Background && a.skills.forkStart(sk.Name)
	if background {
		task.Background = true
		task.OnFinish = func() { a.skills.forkDone(sk.Name) }
	}
	results := a.spawnSubAgents(ctx, []tools.SubTask{task})
	if background && (len(results) == 0 || results[0].Notice != BackgroundNotice) {
		// It ran in the foreground after all (e.g. inside a sub-agent).
		a.skills.forkDone(sk.Name)
	}
	out := fmt.Sprintf("Skill %s ran in a sub-agent (%s).\n", sk.Name, task.Agent) + tools.FormatResults(results)
	return tools.Result{Output: out}
}

// forkAgentName maps a skill's agent field to a subagent definition name:
// Claude Code's names (Explore, general-purpose...) match case-insensitively.
func (a *App) forkAgentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return subagents.BuiltinGeneral
	}
	defs := a.loadSubAgentDefs(a.GetWorkDir(), false)
	if _, ok := defs.Defs[name]; ok {
		return name
	}
	names := defs.Names()
	sort.Strings(names)
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return n
		}
	}
	return name
}

// skillPlan is what a user message invokes: inline skills (already rendered
// into content, with their turn changes) or one forked skill.
type skillPlan struct {
	content      string
	changes      []tools.TurnChange
	fork         *skills.Skill
	forkRendered string
}

// planSkillInvocation handles /<skill> at the start of a user message:
// several inline skills may be stacked (/a /b args), each getting the
// trailing text as its arguments; a forked skill runs alone. Skills named
// later in the message ("... and /deploy ...") are allowed for the
// assistant during this message even if it cannot invoke them on its own.
// An error refuses the message (a skill that is off, or an injected command
// that failed).
func (a *App) planSkillInvocation(ctx context.Context, content string) (skillPlan, error) {
	plan := skillPlan{content: content}
	set := a.loadSkills()
	var permitted []string
	for _, n := range skills.Mentions(content) {
		if sk, ok := set.Get(n); ok && sk.UserCanInvoke() {
			permitted = append(permitted, sk.Name)
		}
	}
	a.skills.setPermitted(permitted)

	name, rest, ok := skills.ParseInvocation(content)
	if !ok {
		return plan, nil
	}
	sk, found := set.Get(name)
	if !found || !sk.UserInvocable {
		return plan, nil // not a skill the user can run: the message goes as written
	}
	if sk.Visibility == skills.VisibilityOff {
		return plan, fmt.Errorf("skill /%s is turned off in the settings (skill_overrides)", sk.Name)
	}
	stack := []skills.Skill{sk}
	if !sk.Forked() {
		for len(stack) < maxStackedSkills {
			n2, r2, ok := skills.ParseInvocation(rest)
			if !ok {
				break
			}
			s2, f := set.Get(n2)
			if !f || !s2.UserCanInvoke() || s2.Forked() {
				break
			}
			stack = append(stack, s2)
			rest = r2
		}
	}
	for _, s := range stack {
		rendered, err := skills.Render(s, rest, a.renderOptions(s, nil))
		if err != nil {
			logger.Error("Skill /%s: %s", s.Name, err.Error())
			return skillPlan{content: content}, fmt.Errorf("skill /%s: %v", s.Name, err)
		}
		a.skills.used(s.Name)
		logger.Info("Skill /%s invoked by the user", s.Name)
		if s.Forked() {
			sc := s
			plan.fork, plan.forkRendered = &sc, rendered
			return plan, nil
		}
		plan.content = agent.WithSkill(plan.content, rendered)
		plan.changes = append(plan.changes, a.skillTurnChange(ctx, s))
	}
	return plan, nil
}

// runPlannedFork runs the forked skill a user message invoked and returns the
// message with its report, for the main agent to relay.
func (a *App) runPlannedFork(ctx context.Context, plan skillPlan) string {
	if plan.fork == nil {
		return plan.content
	}
	logger.Info("Skill /%s runs in a sub-agent (invoked by the user)", plan.fork.Name)
	res := a.runSkillFork(ctx, *plan.fork, plan.forkRendered)
	report := res.Output
	if res.Error != "" {
		report = "Error: " + res.Error
	}
	return agent.WithSkill(plan.content, "The skill runs in a sub-agent (context: fork); tell the user briefly what happened.\n"+report)
}

// maxPreloadedSkills caps the skills a definition preloads (as Claude Code).
const maxPreloadedSkills = 32

// preloadSkills returns the prompt section with the full content of the
// skills a sub-agent definition lists, and a notice for those it could not
// load. Skills the assistant may not invoke (disable-model-invocation or
// hidden by skill_overrides) are not preloaded.
func (a *App) preloadSkills(def *subagents.Definition, workDir string) (string, string) {
	if def == nil || len(def.Skills) == 0 {
		return "", ""
	}
	set := a.loadSkills()
	var b strings.Builder
	var missing []string
	seen := map[string]bool{}
	for _, n := range def.Skills {
		n = strings.TrimPrefix(strings.TrimSpace(n), "/")
		if n == "" || seen[n] || len(seen) >= maxPreloadedSkills {
			continue
		}
		seen[n] = true
		sk, ok := set.Get(n)
		if !ok || !sk.ModelInvocable() {
			missing = append(missing, n)
			continue
		}
		o := a.renderOptions(sk, nil)
		o.ProjectDir = workDir
		rendered, err := skills.Render(sk, "", o)
		if err != nil {
			missing = append(missing, n+" ("+err.Error()+")")
			continue
		}
		b.WriteString("\n\n" + rendered)
	}
	section := ""
	if b.Len() > 0 {
		section = "\n\n## Preloaded skills\nFollow these skills in your work:" + b.String()
	}
	notice := ""
	if len(missing) > 0 {
		notice = "Skills not found or not loadable by the assistant: " + strings.Join(missing, ", ") + "."
	}
	return section, notice
}
