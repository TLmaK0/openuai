package tui

import (
	"context"
	"fmt"
	"strings"

	"openuai/internal/agent"
	"openuai/internal/config"
	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/tools"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

type mode int

const (
	modeChat mode = iota
	modePermission
)

type Model struct {
	// UI components
	input    textinput.Model
	viewport viewport.Model
	spinner  spinner.Model

	// State
	messages     []chatMessage
	mode         mode
	loading      bool
	width        int
	height       int
	inputHistory []string
	historyIdx   int
	savedInput   string

	// Permission dialog
	permTool    string
	permCommand string
	permChoice  int
	permChan    chan PermissionResponseMsg

	// Backend
	cfg         *config.Config
	claude      *llm.ClaudeProvider
	openai      *llm.OpenAIProvider
	oauth       *llm.OAuthFlow
	costTracker *llm.CostTracker
	registry    *tools.Registry
	permissions *agent.PermissionManager
	agent       *agent.Agent
	program     *tea.Program

	// Markdown renderer
	mdRenderer *glamour.TermRenderer
}

func NewModel(cfg *config.Config, claude *llm.ClaudeProvider, openai *llm.OpenAIProvider, oauth *llm.OAuthFlow, costTracker *llm.CostTracker, registry *tools.Registry, permissions *agent.PermissionManager, permChan chan PermissionResponseMsg) Model {
	ti := textinput.New()
	ti.Placeholder = "Give me a task..."
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	vp := viewport.New(80, 20)

	md, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(80),
	)

	return Model{
		input:        ti,
		viewport:     vp,
		spinner:      sp,
		mode:         modeChat,
		historyIdx:   -1,
		permChan:     permChan,
		cfg:          cfg,
		claude:       claude,
		openai:       openai,
		oauth:        oauth,
		costTracker:  costTracker,
		registry:     registry,
		permissions:  permissions,
		mdRenderer:   md,
	}
}

func (m *Model) SetProgram(p *tea.Program) {
	m.program = p
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - 3 // input + status
		m.input.Width = msg.Width - 4
		if m.mdRenderer != nil {
			m.mdRenderer, _ = glamour.NewTermRenderer(
				glamour.WithAutoStyle(),
				glamour.WithWordWrap(msg.Width-4),
			)
		}
		m.viewport.SetContent(m.renderChat())
		m.viewport.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		if m.mode == modePermission {
			return m.updatePermission(msg)
		}
		return m.updateChat(msg)

	case agentStepMsg:
		m.handleStep(msg.step)
		m.viewport.SetContent(m.renderChat())
		m.viewport.GotoBottom()
		return m, nil

	case agentDoneMsg:
		m.loading = false
		// Finalize tool group
		if len(m.messages) > 0 {
			last := &m.messages[len(m.messages)-1]
			if last.role == "tools" && last.active {
				last.active = false
			}
		}
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{role: "error", content: msg.err.Error()})
		}
		// Save session
		if m.agent != nil {
			m.agent.SaveSession(m.cfg.ConfigDir())
		}
		m.viewport.SetContent(m.renderChat())
		m.viewport.GotoBottom()
		return m, nil

	case PermissionRequestMsg:
		m.mode = modePermission
		m.permTool = msg.Tool
		m.permCommand = msg.Command
		m.permChoice = 1 // default to "Allow once"
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) updateChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		val := strings.TrimSpace(m.input.Value())
		if val == "" || m.loading {
			return m, nil
		}
		// Handle commands
		if val == "/clear" {
			m.messages = nil
			m.agent = nil
			m.costTracker.Reset()
			agent.ClearSession(m.cfg.ConfigDir())
			m.input.SetValue("")
			m.viewport.SetContent(m.renderChat())
			return m, nil
		}
		if val == "/quit" || val == "/exit" {
			return m, tea.Quit
		}

		m.inputHistory = append(m.inputHistory, val)
		m.historyIdx = -1
		m.savedInput = ""
		m.input.SetValue("")
		m.messages = append(m.messages, chatMessage{role: "user", content: val})
		m.loading = true
		m.viewport.SetContent(m.renderChat())
		m.viewport.GotoBottom()
		return m, tea.Batch(m.spinner.Tick, m.runAgent(val))

	case "up":
		if len(m.inputHistory) == 0 {
			break
		}
		if m.historyIdx == -1 {
			m.savedInput = m.input.Value()
			m.historyIdx = len(m.inputHistory) - 1
		} else if m.historyIdx > 0 {
			m.historyIdx--
		}
		m.input.SetValue(m.inputHistory[m.historyIdx])
		m.input.CursorEnd()
		return m, nil

	case "down":
		if m.historyIdx == -1 {
			break
		}
		if m.historyIdx < len(m.inputHistory)-1 {
			m.historyIdx++
			m.input.SetValue(m.inputHistory[m.historyIdx])
		} else {
			m.historyIdx = -1
			m.input.SetValue(m.savedInput)
		}
		m.input.CursorEnd()
		return m, nil

	case "pgup":
		m.viewport.HalfViewUp()
		return m, nil
	case "pgdown":
		m.viewport.HalfViewDown()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) updatePermission(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	options := []struct {
		label string
		level agent.PermissionLevel
		ok    bool
	}{
		{"Deny", agent.PermAlways, false},
		{"Allow once", agent.PermAlways, true},
		{"Allow for session", agent.PermSession, true},
		{"Allow forever", agent.PermForever, true},
	}

	switch msg.String() {
	case "left", "h":
		if m.permChoice > 0 {
			m.permChoice--
		}
	case "right", "l":
		if m.permChoice < len(options)-1 {
			m.permChoice++
		}
	case "enter":
		opt := options[m.permChoice]
		m.mode = modeChat
		m.permChan <- PermissionResponseMsg{Level: opt.level, Approved: opt.ok}
		return m, nil
	case "esc":
		m.mode = modeChat
		m.permChan <- PermissionResponseMsg{Approved: false}
		return m, nil
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Status bar
	summary := m.costTracker.Summary()
	provider := m.cfg.Provider
	model := m.cfg.DefaultModel
	cost := fmt.Sprintf("$%.4f", summary.TotalCostUSD)
	tokens := fmt.Sprintf("%dt", summary.TotalInputTokens+summary.TotalOutputTokens)
	status := statusStyle.Render(fmt.Sprintf(" %s/%s  %s  %s", provider, model, costStyle.Render(cost), tokens))

	// Chat viewport
	chat := m.viewport.View()

	// Input area
	var inputLine string
	if m.loading {
		inputLine = fmt.Sprintf(" %s Working...", m.spinner.View())
	} else {
		inputLine = " " + m.input.View()
	}

	// Permission overlay
	if m.mode == modePermission {
		return m.renderPermissionOverlay(status, chat, inputLine)
	}

	return lipgloss.JoinVertical(lipgloss.Left, status, chat, inputLine)
}

func (m Model) renderPermissionOverlay(status, chat, inputLine string) string {
	options := []string{"Deny", "Allow once", "Allow for session", "Allow forever"}
	var opts []string
	for i, o := range options {
		if i == m.permChoice {
			opts = append(opts, permOptionActive.Render("▸ "+o))
		} else {
			opts = append(opts, permOption.Render("  "+o))
		}
	}

	dialog := fmt.Sprintf("Permission Required\n\n%s\n%s\n\n%s",
		errorStyle.Render(m.permTool),
		toolDim.Render(m.permCommand),
		strings.Join(opts, "  "),
	)

	box := permBox.Width(m.width - 10).Render(dialog)
	overlay := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	return overlay
}

func (m Model) renderChat() string {
	if len(m.messages) == 0 && !m.loading {
		return toolDim.Render("\n  Start a conversation — I can execute commands, edit files, and manage git repos\n  Type /clear to reset, /quit to exit\n")
	}

	var lines []string
	for _, msg := range m.messages {
		switch msg.role {
		case "user":
			lines = append(lines, userStyle.Render("> "+msg.content))
			lines = append(lines, "")

		case "assistant":
			rendered := msg.content
			if m.mdRenderer != nil {
				if md, err := m.mdRenderer.Render(msg.content); err == nil {
					rendered = strings.TrimSpace(md)
				}
			}
			lines = append(lines, assistantMark+" "+rendered)
			lines = append(lines, "")

		case "tools":
			lines = append(lines, m.renderToolGroup(msg))

		case "error":
			lines = append(lines, errorStyle.Render("✗ "+msg.content))
			lines = append(lines, "")
		}
	}

	if m.loading {
		lines = append(lines, toolDim.Render("  thinking..."))
	}

	return strings.Join(lines, "\n")
}

func (m Model) renderToolGroup(msg chatMessage) string {
	var lines []string

	for _, step := range msg.steps {
		var icon string
		var style lipgloss.Style
		switch step.status {
		case "done":
			icon = "✓"
			style = toolDone
		case "error":
			icon = "✗"
			style = toolError
		default:
			icon = "⋯"
			style = toolRunning
		}

		header := fmt.Sprintf("  %s %s", style.Render(icon), style.Render(step.tool))
		if step.command != "" {
			header += " " + toolDim.Render(step.command)
		}
		lines = append(lines, header)

		if step.result != "" {
			// Show truncated result
			resultLines := strings.Split(step.result, "\n")
			if len(resultLines) > 5 {
				resultLines = append(resultLines[:5], fmt.Sprintf("... (%d lines)", len(resultLines)))
			}
			for _, rl := range resultLines {
				lines = append(lines, toolDim.Render("    "+rl))
			}
		}
	}

	return strings.Join(lines, "\n")
}

func (m *Model) activeProvider() llm.Provider {
	if m.cfg.Provider == "claude" {
		return m.claude
	}
	return m.openai
}

func (m *Model) ensureAgent() *agent.Agent {
	if m.agent == nil {
		m.agent = agent.New(agent.Config{
			Provider:    m.activeProvider(),
			Model:       m.cfg.DefaultModel,
			Registry:    m.registry,
			Permissions: m.permissions,
			CostTracker: m.costTracker,
			OnStep: func(step agent.StepResult) {
				if m.program != nil {
					m.program.Send(agentStepMsg{step: step})
				}
			},
		})
		m.agent.LoadSession(m.cfg.ConfigDir())
	}
	return m.agent
}

func (m *Model) runAgent(content string) tea.Cmd {
	return func() tea.Msg {
		ag := m.ensureAgent()
		logger.Info("TUI SendMessage: %s", content)
		err := ag.Run(context.Background(), content)
		return agentDoneMsg{err: err}
	}
}

func (m *Model) handleStep(step agent.StepResult) {
	switch step.Type {
	case "text", "done":
		// Finalize tool group
		if len(m.messages) > 0 {
			last := &m.messages[len(m.messages)-1]
			if last.role == "tools" && last.active {
				last.active = false
			}
		}
		if step.Content != "" {
			m.messages = append(m.messages, chatMessage{role: "assistant", content: step.Content})
		}

	case "tool_call":
		entry := toolStep{tool: step.ToolName, command: step.Content, status: "running"}
		if len(m.messages) > 0 {
			last := &m.messages[len(m.messages)-1]
			if last.role == "tools" {
				last.steps = append(last.steps, entry)
				last.active = true
				return
			}
		}
		m.messages = append(m.messages, chatMessage{role: "tools", steps: []toolStep{entry}, active: true})

	case "tool_result":
		if len(m.messages) > 0 {
			last := &m.messages[len(m.messages)-1]
			if last.role == "tools" && len(last.steps) > 0 {
				current := &last.steps[len(last.steps)-1]
				output := step.Content
				// Strip prefix
				if idx := strings.Index(output, "\n"); idx > 0 && strings.HasPrefix(output, "Tool ") {
					output = output[idx+1:]
				}
				rlines := strings.Split(output, "\n")
				if len(rlines) > 10 {
					output = strings.Join(rlines[:10], "\n") + fmt.Sprintf("\n... (%d lines)", len(rlines))
				}
				current.result = output
				current.status = "done"
			}
		}

	case "error":
		if len(m.messages) > 0 {
			last := &m.messages[len(m.messages)-1]
			if last.role == "tools" && len(last.steps) > 0 {
				current := &last.steps[len(last.steps)-1]
				if current.status == "running" {
					current.status = "error"
					current.result = step.Content
					return
				}
			}
		}
		m.messages = append(m.messages, chatMessage{role: "error", content: step.Content})
	}
}
