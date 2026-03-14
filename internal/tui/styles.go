package tui

import "github.com/charmbracelet/lipgloss"

var (
	userStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	assistantMark = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true).Render("◆")
	toolRunning   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	toolDone      = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	toolError     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	toolDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	statusStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	costStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	permBox       = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("3")).
			Padding(1, 2)
	permOption       = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	permOptionActive = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
)
