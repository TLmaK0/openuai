package subagents

import (
	"os"
	"path/filepath"
	"strings"
)

// Persistent memory scopes (same names as Claude Code, OpenUAI folders).
const (
	MemoryUser    = "user"    // <config dir>/agent-memory/<agent>/
	MemoryProject = "project" // <project>/.openuai/agent-memory/<agent>/ (shareable via git)
	MemoryLocal   = "local"   // <project>/.openuai/agent-memory-local/<agent>/ (not for git)
)

// MemoryTools are always given to an agent with memory so it can manage it.
var MemoryTools = []string{"read_file", "write_file", "list_dir"}

const (
	memoryMaxLines = 200
	memoryMaxBytes = 25 * 1024
)

// MemoryDir returns the memory directory of d, or "" when it has no memory.
// userBase is the user-scope base directory (<config dir>/agent-memory).
func MemoryDir(d *Definition, workDir, userBase string) string {
	if d == nil {
		return ""
	}
	switch d.Memory {
	case MemoryUser:
		if userBase == "" {
			return ""
		}
		return filepath.Join(userBase, d.Name)
	case MemoryProject, MemoryLocal:
		if workDir == "" {
			return ""
		}
		sub := "agent-memory"
		if d.Memory == MemoryLocal {
			sub = "agent-memory-local"
		}
		return filepath.Join(workDir, ".openuai", sub, d.Name)
	}
	return ""
}

// MemoryPrompt creates dir if needed and returns the system-prompt section
// that explains the memory and includes the start of its MEMORY.md.
func MemoryPrompt(dir string) string {
	if dir == "" {
		return ""
	}
	_ = os.MkdirAll(dir, 0o700)
	index := filepath.Join(dir, "MEMORY.md")
	var b strings.Builder
	b.WriteString("\n\n## Your persistent memory\n\n")
	b.WriteString("You have a memory directory that survives across conversations: " + dir + "\n")
	b.WriteString("Before starting, consult it for knowledge from previous runs. When you learn something reusable " +
		"(code paths, patterns, conventions, decisions, recurring problems), save it there with write_file. " +
		"Keep " + index + " as a concise index (under 200 lines) pointing to topic files.\n")
	data, err := os.ReadFile(index)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		b.WriteString("\nMEMORY.md is empty: nothing saved yet.")
		return b.String()
	}
	text := string(data)
	truncated := false
	if lines := strings.Split(text, "\n"); len(lines) > memoryMaxLines {
		text = strings.Join(lines[:memoryMaxLines], "\n")
		truncated = true
	}
	if len(text) > memoryMaxBytes {
		text = text[:memoryMaxBytes]
		truncated = true
	}
	b.WriteString("\n### MEMORY.md\n\n" + text)
	if truncated {
		b.WriteString("\n\n[MEMORY.md was cut here: it is too long. Curate it to keep it short.]")
	}
	return b.String()
}
