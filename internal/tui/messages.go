package tui

import (
	"openuai/internal/agent"
	"openuai/internal/llm"
)

// Messages flowing through bubbletea

type agentStepMsg struct {
	step agent.StepResult
}

type agentDoneMsg struct {
	err error
}

// PermissionRequestMsg is sent from the agent goroutine to the TUI
type PermissionRequestMsg struct {
	Tool    string
	Command string
}

// PermissionResponseMsg is sent from the TUI back to the agent goroutine
type PermissionResponseMsg struct {
	Level    agent.PermissionLevel
	Approved bool
}

type costUpdateMsg struct {
	summary llm.CostSummary
}

// Chat message types for display
type chatMessage struct {
	role    string // "user", "assistant", "tools", "error"
	content string
	steps   []toolStep
	active  bool // tool group still running
}

type toolStep struct {
	tool    string
	command string
	result  string
	status  string // "running", "done", "error"
}
