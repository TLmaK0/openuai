package subagents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// InstructionFile is one instruction file (AGENTS.md) loaded into a prompt.
type InstructionFile struct {
	Path      string `json:"path"`
	Scope     string `json:"scope"` // ScopeUser or ScopeProject
	Size      int    `json:"size"`  // bytes on disk
	Truncated bool   `json:"truncated"`
	Content   string `json:"-"` // what goes in the prompt (capped, trimmed)
}

// UserInstructionsFile is the user's own instructions file in OpenUAI's
// config folder.
func UserInstructionsFile(configDir string) string {
	if configDir == "" {
		return ""
	}
	return filepath.Join(configDir, "AGENTS.md")
}

// readInstructionFile reads path capped at maxInstructionBytes, with a
// visible note when it was cut. ok is false when it cannot be read.
func readInstructionFile(path, scope string) (InstructionFile, bool) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return InstructionFile{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return InstructionFile{}, false
	}
	f := InstructionFile{Path: path, Scope: scope, Size: len(data)}
	if len(data) > maxInstructionBytes {
		cut := maxInstructionBytes
		for cut > 0 && !utf8.RuneStart(data[cut]) {
			cut--
		}
		data = data[:cut]
		f.Truncated = true
	}
	f.Content = strings.TrimSpace(string(data))
	if f.Truncated {
		f.Content += fmt.Sprintf("\n\n[Truncated: this file is %d bytes; only the first %d bytes were loaded. Read the file for the rest.]", f.Size, len(data))
	}
	return f, true
}

// LoadInstructions returns the instruction files of the main conversation:
// the user's file (userFile, may be empty) first, then every AGENTS.md from
// the repository root down to workDir, the closest one last.
func LoadInstructions(userFile, workDir string) []InstructionFile {
	var out []InstructionFile
	if userFile != "" {
		if f, ok := readInstructionFile(userFile, ScopeUser); ok {
			out = append(out, f)
		}
	}
	for _, d := range projectChain(workDir) {
		p := filepath.Join(d, "AGENTS.md")
		if p == userFile {
			continue
		}
		if f, ok := readInstructionFile(p, ScopeProject); ok {
			out = append(out, f)
		}
	}
	return out
}

// formatInstructionFiles renders files as "Contents of <path>:" blocks.
func formatInstructionFiles(files []InstructionFile) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "Contents of %s:\n\n%s\n\n", f.Path, f.Content)
	}
	return strings.TrimSpace(b.String())
}

// MainInstructionsPrompt is the system prompt section for the main
// conversation's instruction files, or "" when there are none.
func MainInstructionsPrompt(files []InstructionFile) string {
	if len(files) == 0 {
		return ""
	}
	var user, project []InstructionFile
	for _, f := range files {
		if f.Scope == ScopeUser {
			user = append(user, f)
		} else {
			project = append(project, f)
		}
	}
	p := "## Instructions (AGENTS.md)\n\nFollow these instructions. They go from the most general to the most specific: when they conflict, the later ones win."
	if len(user) > 0 {
		p += "\n\n### User instructions\n\n" + formatInstructionFiles(user)
	}
	if len(project) > 0 {
		p += "\n\n### Project instructions\n\n" + formatInstructionFiles(project)
	}
	return p
}
