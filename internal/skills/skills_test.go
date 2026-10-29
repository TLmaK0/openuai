package skills

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func render(t *testing.T, sk Skill, args string, o RenderOptions) string {
	t.Helper()
	out, err := Render(sk, args, o)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLoadPrecedenceAndWarnings(t *testing.T) {
	cfg, work := t.TempDir(), t.TempDir()
	write(t, filepath.Join(cfg, "skills", "deploy", "SKILL.md"), "---\ndescription: user deploy\n---\nUSER")
	write(t, filepath.Join(work, ".openuai", "skills", "deploy", "SKILL.md"), "---\ndescription: project deploy\n---\nPROJECT")
	write(t, filepath.Join(work, ".openuai", "skills", "style", "SKILL.md"), "---\nname: house-style\ndescription: Our style\ndisable-model-invocation: yes\n---\nUse tabs.")
	write(t, filepath.Join(work, ".openuai", "skills", "broken", "SKILL.md"), "---\ndescription: [unclosed\n---\n# Broken but loads\nbody")
	write(t, filepath.Join(work, ".openuai", "skills", "empty", "README.md"), "no SKILL.md here")
	write(t, filepath.Join(work, ".openuai", "skills", "synced", "SKILL.md"), "reserved")
	s := Load(cfg, work)
	if len(s.Skills) != 3 {
		t.Fatalf("skills: %+v", s.Skills)
	}
	d, _ := s.Get("deploy")
	if d.Source != "user" || d.Body != "USER" {
		t.Fatalf("user skill must win: %+v", d)
	}
	st, ok := s.Get("/house-style")
	if !ok || !st.DisableModelInvocation || st.Description != "Our style" {
		t.Fatalf("style: %+v", st)
	}
	if byDir, ok := s.Get("style"); !ok || byDir.Name != "house-style" {
		t.Fatalf("the folder name must also invoke the skill: %+v", byDir)
	}
	b, ok := s.Get("broken")
	if !ok || b.Description != "Broken but loads" || len(s.Warnings) != 2 {
		t.Fatalf("broken frontmatter: %+v warnings=%v", b, s.Warnings)
	}
	list := s.Describe()
	if !strings.Contains(list, "- deploy: user deploy") || strings.Contains(list, "house-style") {
		t.Fatalf("listing: %s", list)
	}
}

func TestParseFrontmatterFields(t *testing.T) {
	sk, warn := Parse([]byte(`---
description: d
arguments: issue branch
allowed-tools: Bash(git add *) Bash(git commit *), Read
disallowed-tools:
  - WebFetch
  - mcp__slack__post
model: haiku
effort: LOW
context: fork
agent: Explore
background: no
paths: "src/**/*.go, *.md"
shell: powershell
hooks:
  PreToolUse:
    - matcher: bash
metadata:
  team: core
license: MIT
compatibility: linux
unknown-field: ignored
---
body`), "x")
	if warn != "" {
		t.Fatal(warn)
	}
	want := Skill{
		Name: "x", DirName: "x", Description: "d", Arguments: []string{"issue", "branch"}, UserInvocable: true,
		AllowedTools: []string{"Bash(git add *)", "Bash(git commit *)", "Read"}, DisallowedTools: []string{"WebFetch", "mcp__slack__post"},
		Model: "haiku", Effort: "low", Context: "fork", Agent: "Explore", Background: false,
		Paths: []string{"src/**/*.go", "*.md"}, Shell: "powershell",
		Hooks:    map[string]any{"PreToolUse": []any{map[string]any{"matcher": "bash"}}},
		Metadata: map[string]any{"team": "core"}, License: "MIT", Compatibility: "linux",
		Visibility: VisibilityOn, Body: "body",
	}
	if !reflect.DeepEqual(sk, want) {
		t.Fatalf("got  %+v\nwant %+v", sk, want)
	}
	def, _ := Parse([]byte("---\ncontext: fork\nmetadata: notamap\n---\nb"), "y")
	if !def.Background || def.Metadata != nil {
		t.Fatalf("background defaults to true and a non-map metadata is dropped: %+v", def)
	}
}

func TestRenderArguments(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "scripts", "run.sh"), "echo")
	sk := Skill{Name: "fix", Dir: dir, Body: "Fix issue $0 on $ARGUMENTS[1]; all: $ARGUMENTS. Run ${OPENUAI_SKILL_DIR}/scripts/run.sh in ${OPENUAI_PROJECT_DIR} (${OPENUAI_SESSION_ID}, ${OPENUAI_EFFORT})"}
	out := render(t, sk, `123 "main branch"`, RenderOptions{ProjectDir: "/proj", SessionID: "sess", Effort: "high"})
	for _, want := range []string{"Fix issue 123 on main branch", `all: 123 "main branch"`, dir + "/scripts/run.sh in /proj (sess, high)", "Supporting files", filepath.Join(dir, "scripts", "run.sh")} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if out := render(t, Skill{Name: "x", Dir: t.TempDir(), Body: "No placeholders"}, "hello", RenderOptions{}); !strings.Contains(out, "ARGUMENTS: hello") {
		t.Fatalf("unused args must be appended: %s", out)
	}
	// An indexed placeholder with no argument stays and does not count.
	if out := render(t, Skill{Name: "x", Dir: t.TempDir(), Body: "A $2 B"}, "one", RenderOptions{}); !strings.Contains(out, "A $2 B") || !strings.Contains(out, "ARGUMENTS: one") {
		t.Fatalf("missing index: %s", out)
	}
}

func TestRenderNamedArgumentsAndEscapes(t *testing.T) {
	sk := Skill{Name: "n", Dir: t.TempDir(), Arguments: []string{"issue", "branch", "extra"},
		Body: `Issue $issue on $branch, extra=[$extra], $issue_x, $other, costs \$1.00, \$issue, \\$0, $0`}
	out := render(t, sk, `42 "$ARGUMENTS from x"`, RenderOptions{})
	want := `Issue 42 on $ARGUMENTS from x, extra=[], $issue_x, $other, costs $1.00, $issue, \\42, 42`
	if !strings.Contains(out, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if strings.Contains(out, "ARGUMENTS:") {
		t.Fatal("named placeholders received the arguments")
	}
	// A named placeholder counts even without its argument.
	if out := render(t, Skill{Name: "n", Dir: t.TempDir(), Arguments: []string{"a", "b"}, Body: "[$b]"}, "x", RenderOptions{}); !strings.Contains(out, "[]") || strings.Contains(out, "ARGUMENTS:") {
		t.Fatal(out)
	}
}

func TestRenderInjection(t *testing.T) {
	var ran []string
	run := func(c, sh string) ShellResult {
		ran = append(ran, c)
		switch {
		case strings.HasPrefix(c, "grep"):
			return ShellResult{ExitCode: 1}
		case strings.HasPrefix(c, "false"):
			return ShellResult{ExitCode: 1, Output: "boom"}
		}
		return ShellResult{Output: "OUT(" + c + ")\n"}
	}
	sk := Skill{Name: "inj", Dir: "/sk", Body: "Diff: !`git diff $0`\nKEY=!`not run`\n!`grep x`\n```!\necho a\necho b\n```\nend"}
	out := render(t, sk, "HEAD", RenderOptions{Run: run})
	for _, want := range []string{"Diff: OUT(git diff HEAD)", "KEY=!`not run`", "OUT(echo a\necho b)\nend"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if len(ran) != 3 {
		t.Fatalf("ran: %q", ran)
	}
	// Output is not re-scanned, and arguments cannot create commands.
	ran = nil
	out = render(t, Skill{Name: "x", Dir: "/sk", Body: "A: $ARGUMENTS"}, "!`rm -rf x`", RenderOptions{Run: run})
	if len(ran) != 0 || !strings.Contains(out, "!`rm -rf x`") {
		t.Fatalf("arguments ran a command: %q", ran)
	}
	// A failure aborts the whole invocation.
	_, err := Render(Skill{Name: "x", Dir: "/sk", Body: "!`echo ok`\n!`false`"}, "", RenderOptions{Run: run})
	if err == nil || !strings.Contains(err.Error(), "Shell command failed for pattern") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err: %v", err)
	}
	// Permission: a command that is not allowed aborts without running.
	ran = nil
	_, err = Render(Skill{Name: "x", Dir: "/sk", Body: "!`echo hi`"}, "", RenderOptions{Run: run, CanRun: func(string) bool { return false }})
	if err == nil || !strings.Contains(err.Error(), "Shell command permission check failed") || len(ran) != 0 {
		t.Fatalf("err: %v ran=%q", err, ran)
	}
	// Disabled by policy.
	out = render(t, Skill{Name: "x", Dir: "/sk", Body: "v: !`echo hi`"}, "", RenderOptions{Run: run, ShellDisabled: true})
	if !strings.Contains(out, "v: "+ShellDisabledText) {
		t.Fatal(out)
	}
}

func TestRunShellReal(t *testing.T) {
	if _, _, err := shellFor(""); err != nil {
		t.Skip("no shell")
	}
	dir := t.TempDir()
	out := render(t, Skill{Name: "x", Dir: dir, Body: "pwd: !`pwd`; err: !`echo e 1>&2`"}, "", RenderOptions{ProjectDir: dir})
	if !strings.Contains(out, "err: e") || !strings.Contains(out, "pwd: "+dir) {
		t.Fatal(out)
	}
	if _, err := Render(Skill{Name: "x", Dir: dir, Body: "!`exit 3`"}, "", RenderOptions{ProjectDir: dir}); err == nil {
		t.Fatal("exit 3 must fail")
	}
}

func TestRenderFileReferences(t *testing.T) {
	proj, skdir := t.TempDir(), t.TempDir()
	write(t, filepath.Join(proj, "docs", "guide.md"), "GUIDE TEXT")
	write(t, filepath.Join(skdir, "ref.md"), "REF TEXT")
	out := render(t, Skill{Name: "x", Dir: skdir, Body: "See @docs/guide.md and @ref.md, not mail@example.com or @missing.md"}, "", RenderOptions{ProjectDir: proj})
	if !strings.Contains(out, "GUIDE TEXT") || !strings.Contains(out, "REF TEXT") || strings.Count(out, "<file ") != 2 {
		t.Fatal(out)
	}
}

func TestParseInvocationAndMentions(t *testing.T) {
	for in, want := range map[string][2]string{
		"/deploy staging now":   {"deploy", "staging now"},
		"  /style":              {"style", ""},
		"/a\nline two":          {"a", "line two"},
		"/apps/web:deploy prod": {"apps/web:deploy", "prod"},
	} {
		n, a, ok := ParseInvocation(in)
		if !ok || n != want[0] || a != want[1] {
			t.Fatalf("%q: %q %q %v", in, n, a, ok)
		}
	}
	for _, in := range []string{"hola /deploy", "/", "/home/x/file", "plain"} {
		if _, _, ok := ParseInvocation(in); ok {
			t.Fatalf("%q must not be an invocation", in)
		}
	}
	if got := Mentions("/first go ahead and /deploy to staging, not /x, or (/y)"); !reflect.DeepEqual(got, []string{"deploy"}) {
		t.Fatalf("mentions: %q", got)
	}
}

func TestToolRules(t *testing.T) {
	sk := Skill{Dir: "/skills/chart"}
	rules := ToolRules([]string{"Bash(git add *)", "Bash(${OPENUAI_SKILL_DIR}/render.sh *)", "Read", "Grep", "mcp__slack__post", "Bash(npm:*)", "web_fetch"}, sk, "/p")
	cases := []struct {
		tool, arg string
		want      bool
	}{
		{"bash", "git add .", true},
		{"bash", "git add", true},
		{"bash", "git push", false},
		{"bash", "git add . && rm -rf /", true}, // best effort, like a prefix rule
		{"git_add", "x", true},
		{"git_commit", "x", false},
		{"bash", "/skills/chart/render.sh data.csv", true},
		{"bash", "npm test", true},
		{"read_file", "/etc/hosts", true},
		{"search_files", "*.go", true},
		{"mcp_slack_post", "", true},
		{"web_fetch", "https://x", true},
		{"write_file", "/x", false},
	}
	for _, c := range cases {
		if got := AnyMatch(rules, c.tool, c.arg); got != c.want {
			t.Errorf("%s(%s) = %v, want %v", c.tool, c.arg, got, c.want)
		}
	}
	if got := ToolNames(ToolRules([]string{"WebFetch", "Bash(rm *)"}, sk, "")); !reflect.DeepEqual(got, []string{"web_fetch"}) {
		t.Fatalf("names: %q", got)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"src/**/*.ts", "src/a/b/c.ts", true},
		{"src/**/*.ts", "src/c.ts", true},
		{"src/*.ts", "src/a/c.ts", false},
		{"*.md", "docs/readme.md", true},
		{"*.{js,ts}", "a/b.ts", true},
		{"docs/*.md", "other/docs/x.md", false},
		{"**/*.go", "main.go", true},
		{"*.go", "../outside.go", false},
	}
	for _, c := range cases {
		if got := GlobMatch(c.pat, c.path); got != c.want {
			t.Errorf("%s ~ %s = %v", c.pat, c.path, got)
		}
	}
}

func TestListingBudgetVisibilityAndPaths(t *testing.T) {
	s := &Set{Skills: []Skill{
		{Name: "a", Description: strings.Repeat("a", 100), Visibility: VisibilityOn},
		{Name: "b", Description: strings.Repeat("b", 100), Visibility: VisibilityOn},
		{Name: "c", Description: "use <branch>", Visibility: VisibilityNameOnly},
		{Name: "d", Description: "user only", Visibility: VisibilityUserOnly},
		{Name: "e", Description: "off", Visibility: VisibilityOff},
		{Name: "go-rules", Description: "go", Paths: []string{"**/*.go"}, Visibility: VisibilityOn},
		{Name: "long", Description: strings.Repeat("x", 50), WhenToUse: "<when>", Visibility: VisibilityOn},
	}}
	l, over := s.Listing(ListOptions{MaxDescChars: 20})
	if over || !strings.Contains(l, "- c\n") || strings.Contains(l, "- d") || strings.Contains(l, "- e") || strings.Contains(l, "go-rules") {
		t.Fatalf("listing:\n%s", l)
	}
	if !strings.Contains(l, "- long: "+strings.Repeat("x", 20)+"…") {
		t.Fatalf("cap:\n%s", l)
	}
	l, _ = s.Listing(ListOptions{PathActive: func(sk Skill) bool { return sk.MatchesPath("cmd/main.go") }})
	if !strings.Contains(l, "- go-rules: go") || !strings.Contains(l, "&lt;when&gt;") {
		t.Fatalf("paths/escape:\n%s", l)
	}
	// Over budget: the least used lose their descriptions first; names stay.
	l, over = s.Listing(ListOptions{Budget: 160, Usage: map[string]int{"a": 5, "long": 2}})
	if !over || !strings.Contains(l, "- a: aaa") || strings.Contains(l, "- b: ") || !strings.Contains(l, "- b\n") {
		t.Fatalf("budget:\n%s", l)
	}
}

func TestOverridesAndNested(t *testing.T) {
	work := t.TempDir()
	write(t, filepath.Join(work, ".openuai", "skills", "deploy", "SKILL.md"), "---\ndescription: root deploy\n---\nROOT")
	write(t, filepath.Join(work, ".openuai", "skills", "legacy", "SKILL.md"), "---\ndescription: legacy\n---\nL")
	web := filepath.Join(work, "apps", "web")
	write(t, filepath.Join(web, ".openuai", "skills", "deploy", "SKILL.md"), "---\ndescription: web deploy\n---\nWEB")
	write(t, filepath.Join(web, ".openuai", "skills", "lint", "SKILL.md"), "---\ndescription: web lint\n---\nLINT")

	if s := LoadWith(Options{WorkDir: work}); len(s.Skills) != 2 {
		t.Fatalf("nested skills load only once activated: %+v", s.Skills)
	}
	s := LoadWith(Options{WorkDir: work, NestedDirs: []string{web}, Overrides: map[string]string{"legacy": "off"}})
	root, _ := s.Get("deploy")
	nested, ok := s.Get("apps/web:deploy")
	lint, ok2 := s.Get("lint")
	if root.Body != "ROOT" || !ok || nested.Body != "WEB" || !ok2 || lint.Scope != "apps/web" {
		t.Fatalf("root=%+v nested=%+v lint=%+v", root, nested, lint)
	}
	legacy, _ := s.Get("legacy")
	if legacy.Visibility != VisibilityOff || legacy.UserCanInvoke() || legacy.ModelInvocable() {
		t.Fatalf("override: %+v", legacy)
	}
	if l := s.Describe(); !strings.Contains(l, "apps/web:deploy") || !strings.Contains(l, "share a name") || strings.Contains(l, "legacy") {
		t.Fatalf("listing:\n%s", l)
	}
}

func TestProjectSkillDirsMonorepoAndWorktree(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, ".openuai", "skills", "root-skill", "SKILL.md"), "root")
	pkg := filepath.Join(repo, "packages", "frontend")
	write(t, filepath.Join(pkg, ".openuai", "skills", "fe", "SKILL.md"), "fe")
	s := Load("", pkg)
	if _, ok := s.Get("root-skill"); !ok {
		t.Fatal("skills from parent directories up to the repo root must load")
	}
	if _, ok := s.Get("fe"); !ok {
		t.Fatal("own skills")
	}
	// A linked worktree without .openuai/skills uses the main checkout's.
	wt := t.TempDir()
	write(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n")
	if _, ok := Load("", wt).Get("root-skill"); !ok {
		t.Fatalf("worktree fallback: %v", ProjectSkillDirs(wt))
	}
}

func TestSplitToolList(t *testing.T) {
	got := SplitToolList("Bash(git add *) Bash(git commit *),Read  Grep")
	if !reflect.DeepEqual(got, []string{"Bash(git add *)", "Bash(git commit *)", "Read", "Grep"}) {
		t.Fatalf("%q", got)
	}
}

var _ = errors.New
