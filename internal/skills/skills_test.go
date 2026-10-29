package skills

import (
	"os"
	"path/filepath"
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

func TestLoadPrecedenceAndWarnings(t *testing.T) {
	cfg, work := t.TempDir(), t.TempDir()
	write(t, filepath.Join(cfg, "skills", "deploy", "SKILL.md"), "---\ndescription: user deploy\n---\nUSER")
	write(t, filepath.Join(work, ".openuai", "skills", "deploy", "SKILL.md"), "---\ndescription: project deploy\n---\nPROJECT")
	write(t, filepath.Join(work, ".openuai", "skills", "style", "SKILL.md"), "---\nname: house-style\ndescription: Our style\ndisable-model-invocation: yes\n---\nUse tabs.")
	write(t, filepath.Join(work, ".openuai", "skills", "broken", "SKILL.md"), "---\ndescription: [unclosed\n---\n# Broken but loads\nbody")
	write(t, filepath.Join(work, ".openuai", "skills", "empty", "README.md"), "no SKILL.md here")
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
	b, ok := s.Get("broken")
	if !ok || b.Description != "Broken but loads" || len(s.Warnings) != 1 {
		t.Fatalf("broken frontmatter: %+v warnings=%v", b, s.Warnings)
	}
	list := s.Describe()
	if !strings.Contains(list, "- deploy: user deploy") || strings.Contains(list, "house-style") {
		t.Fatalf("listing: %s", list)
	}
}

func TestRender(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "scripts", "run.sh"), "echo")
	sk := Skill{Name: "fix", Dir: dir, Body: "Fix issue $0 on $ARGUMENTS[1]; all: $ARGUMENTS. Run ${OPENUAI_SKILL_DIR}/scripts/run.sh in ${OPENUAI_PROJECT_DIR}"}
	out := Render(sk, `123 "main branch"`, "/proj")
	for _, want := range []string{"Fix issue 123 on main branch", `all: 123 "main branch"`, dir + "/scripts/run.sh in /proj", "Supporting files", filepath.Join(dir, "scripts", "run.sh")} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if out := Render(Skill{Name: "x", Dir: t.TempDir(), Body: "No placeholders"}, "hello", ""); !strings.Contains(out, "ARGUMENTS: hello") {
		t.Fatalf("unused args must be appended: %s", out)
	}
}

func TestParseInvocation(t *testing.T) {
	for in, want := range map[string][2]string{
		"/deploy staging now": {"deploy", "staging now"},
		"  /style":            {"style", ""},
	} {
		n, a, ok := ParseInvocation(in)
		if !ok || n != want[0] || a != want[1] {
			t.Fatalf("%q: %q %q %v", in, n, a, ok)
		}
	}
	for _, in := range []string{"hola /deploy", "/", "/home/x/file", "plain"} {
		if n, _, ok := ParseInvocation(in); ok && n != "home" {
			t.Fatalf("%q must not be an invocation", in)
		}
	}
}
