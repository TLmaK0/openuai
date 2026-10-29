package subagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMemory(t *testing.T) {
	d, err := Parse([]byte("---\nname: rev\ndescription: x\nmemory: Project\n---\nbody"))
	if err != nil || d.Memory != "project" {
		t.Fatalf("got %+v %v", d, err)
	}
	if _, err := Parse([]byte("---\nname: rev\ndescription: x\nmemory: global\n---\nbody")); err == nil {
		t.Fatal("invalid memory must be rejected")
	}
}

func TestMemoryDirAndPrompt(t *testing.T) {
	wd, ub := t.TempDir(), t.TempDir()
	d := &Definition{Name: "rev"}
	if MemoryDir(d, wd, ub) != "" {
		t.Fatal("no memory by default")
	}
	d.Memory = MemoryUser
	if got := MemoryDir(d, wd, ub); got != filepath.Join(ub, "rev") {
		t.Fatalf("user: %s", got)
	}
	d.Memory = MemoryLocal
	if got := MemoryDir(d, wd, ub); got != filepath.Join(wd, ".openuai", "agent-memory-local", "rev") {
		t.Fatalf("local: %s", got)
	}
	d.Memory = MemoryProject
	dir := MemoryDir(d, wd, ub)
	if dir != filepath.Join(wd, ".openuai", "agent-memory", "rev") {
		t.Fatalf("project: %s", dir)
	}
	if p := MemoryPrompt(dir); !strings.Contains(p, "empty") {
		t.Fatalf("empty memory: %s", p)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatal("memory dir must be created")
	}
	long := strings.Repeat("line\n", 300)
	_ = os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("REMEMBER_ME\n"+long), 0o600)
	p := MemoryPrompt(dir)
	if !strings.Contains(p, "REMEMBER_ME") || !strings.Contains(p, "was cut") {
		t.Fatalf("memory content/truncation missing")
	}
}
