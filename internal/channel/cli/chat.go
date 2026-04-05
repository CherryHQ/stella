package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/pkg/ai"
)

// streamStartMsg carries the stream channel from the agent.
type streamStartMsg struct {
	stream <-chan runner.Event
}

// streamChunkMsg carries a text delta from the agent stream.
type streamChunkMsg string

// streamToolMsg carries a tool-use event from the agent stream.
type streamToolMsg struct {
	tool   string
	status string
	input  string
	detail string
}

// streamDoneMsg signals the stream has finished.
type streamDoneMsg struct{}

// streamErrMsg carries a streaming error.
type streamErrMsg struct{ err error }

// compactDoneMsg signals that session compaction finished.
type compactDoneMsg struct {
	summary string
	err     error
}

type chatModel struct {
	ctx      context.Context
	pool     *agent.Pool
	textarea textarea.Model
	viewport viewport.Model
	stream   <-chan runner.Event

	sessionID   string
	provider    string
	model       string
	history     *strings.Builder
	streaming   bool
	status      string
	width       int
	height      int
	ready       bool
	switchModel channel.ModelSwitchFunc
	listModels  channel.ModelListFunc

	// Slash command completion
	completing     bool
	completions    []slashCommand
	completeCursor int

	// Tool use tracking
	toolStartTime time.Time

	// Markdown rendering: track current response segments
	historyPrefix string           // rendered history before current response
	currentRaw    *strings.Builder // raw markdown text of current streaming segment
	mdRenderer    *glamour.TermRenderer

	// Model picker
	picking        bool
	models         []modelOption
	filteredModels []modelOption
	modelCursor    int
	modelFilter    string
}

func newChatModel(ctx context.Context, pool *agent.Pool, provider, model string, listFn channel.ModelListFunc, switchFn channel.ModelSwitchFunc, userID ...int64) chatModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message... (Enter to send, Alt+Enter for newline)"
	ta.Focus()
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.SetHeight(1)

	// Resolve session: resume the most recent active session, or create a new one.
	sessionID := resolveSession(pool, userID...)

	m := chatModel{
		ctx:         ctx,
		pool:        pool,
		textarea:    ta,
		sessionID:   sessionID,
		provider:    provider,
		model:       model,
		history:     &strings.Builder{},
		listModels:  listFn,
		switchModel: switchFn,
		currentRaw:  &strings.Builder{},
	}

	// Restore conversation display from persisted history.
	if rendered := renderResumedHistory(pool, sessionID); rendered != "" {
		m.history.WriteString(rendered)
		m.historyPrefix = m.history.String()
	}

	return m
}

const cliChannel = channel.PlatformCLI

// resolveSession returns the most recently active CLI session ID,
// or creates a new session if none exist.
func resolveSession(pool *agent.Pool, userID ...int64) string {
	info, err := pool.ResolveSession(cliChannel, userID...)
	if err != nil {
		return "cli-session"
	}
	return info.ID
}

// renderResumedHistory builds the viewport content from a session's persisted events.
// Only user messages and assistant text are rendered; tool calls are omitted for brevity.
func renderResumedHistory(pool *agent.Pool, sessionID string) string {
	msgs := pool.History(sessionID)
	if len(msgs) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(systemStyle.Render("[session resumed]") + "\n\n")

	for _, msg := range msgs {
		switch m := msg.(type) {
		case ai.UserMessage:
			text := messageText(m.Content)
			if text == "" || text == "[Previous conversation summary]" {
				continue
			}
			b.WriteString(userStyle.Render("You") + "\n")
			b.WriteString(userBorderStyle.Render(chatTextStyle.Render(text)) + "\n\n")

		case ai.AssistantMessage:
			text := ai.FlattenText(m.Content)
			if text == "" {
				continue
			}
			b.WriteString(agentStyle.Render("Anna") + "\n")
			b.WriteString(agentBorderStyle.Render(text) + "\n\n")
		}
	}

	return b.String()
}

// messageText extracts display text from user message content.
func messageText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []ai.ContentBlock:
		return ai.FlattenText(c)
	default:
		return fmt.Sprintf("%v", content)
	}
}

func (m chatModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.picking {
			return m.handlePickingKey(msg)
		}

		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit

		case tea.KeyTab:
			if m.completing && len(m.completions) > 0 {
				selected := m.completions[m.completeCursor]
				m.textarea.SetValue(selected.name)
				m.completing = false
				m.completions = nil
				m.resize()
				return m, nil
			}

		case tea.KeyUp:
			if m.completing && len(m.completions) > 0 {
				if m.completeCursor > 0 {
					m.completeCursor--
				}
				return m, nil
			}

		case tea.KeyDown:
			if m.completing && len(m.completions) > 0 {
				if m.completeCursor < len(m.completions)-1 {
					m.completeCursor++
				}
				return m, nil
			}

		case tea.KeyEsc:
			if m.completing {
				m.completing = false
				m.completions = nil
				m.resize()
				return m, nil
			}

		case tea.KeyEnter:
			if m.streaming {
				break
			}
			// If completing, accept and submit the selected command.
			if m.completing && len(m.completions) > 0 {
				selected := m.completions[m.completeCursor]
				m.textarea.Reset()
				m.completing = false
				m.completions = nil
				m.resize()
				cmd := m.handleInput(selected.name)
				return m, cmd
			}
			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				break
			}
			m.textarea.Reset()
			m.completing = false
			m.completions = nil
			cmd := m.handleInput(input)
			return m, cmd
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil

	case streamStartMsg:
		m.stream = msg.stream
		return m, waitNextChunk(m.stream)

	case streamChunkMsg:
		m.status = ""
		m.currentRaw.WriteString(string(msg))
		m.refreshViewport()
		return m, waitNextChunk(m.stream)

	case streamToolMsg:
		label := msg.tool
		if msg.input != "" {
			label += ": " + msg.input
		}
		switch msg.status {
		case "running":
			m.toolStartTime = time.Now()
			m.status = "Running " + label + "..."
		case "done":
			// Flush current markdown segment into prefix before tool line
			if m.currentRaw.Len() > 0 {
				m.historyPrefix += agentBorderStyle.Render(m.renderMarkdown(m.currentRaw.String())) + "\n"
				m.currentRaw.Reset()
			}
			elapsed := channel.FormatDuration(time.Since(m.toolStartTime))
			m.status = ""
			m.historyPrefix += toolDoneStyle.Render(fmt.Sprintf("    ✓ %s (%s)", label, elapsed)) + "\n"
		case "error":
			if m.currentRaw.Len() > 0 {
				m.historyPrefix += agentBorderStyle.Render(m.renderMarkdown(m.currentRaw.String())) + "\n"
				m.currentRaw.Reset()
			}
			elapsed := channel.FormatDuration(time.Since(m.toolStartTime))
			m.status = ""
			line := fmt.Sprintf("    ✗ %s (%s)", label, elapsed)
			if msg.detail != "" {
				line += " — " + msg.detail
			}
			m.historyPrefix += toolErrorStyle.Render(line) + "\n"
		}
		m.refreshViewport()
		return m, waitNextChunk(m.stream)

	case streamDoneMsg:
		m.streaming = false
		m.status = ""
		m.stream = nil
		// Finalize: flush remaining markdown into history with agent border
		if m.currentRaw.Len() > 0 {
			m.historyPrefix += agentBorderStyle.Render(m.renderMarkdown(m.currentRaw.String()))
			m.currentRaw.Reset()
		}
		m.historyPrefix += "\n\n"
		m.history.Reset()
		m.history.WriteString(m.historyPrefix)
		m.viewport.SetContent(m.history.String())
		m.viewport.GotoBottom()
		m.textarea.Focus()
		return m, nil

	case streamErrMsg:
		m.streaming = false
		m.status = ""
		m.stream = nil
		if m.currentRaw.Len() > 0 {
			m.historyPrefix += agentBorderStyle.Render(m.renderMarkdown(m.currentRaw.String()))
			m.currentRaw.Reset()
		}
		m.historyPrefix += "\n" + errorStyle.Render("error: "+msg.err.Error()) + "\n\n"
		m.history.Reset()
		m.history.WriteString(m.historyPrefix)
		m.viewport.SetContent(m.history.String())
		m.viewport.GotoBottom()
		m.textarea.Focus()
		return m, nil

	case compactDoneMsg:
		m.streaming = false
		m.status = ""
		if msg.err != nil {
			m.history.WriteString(errorStyle.Render("compaction failed: "+msg.err.Error()) + "\n\n")
		} else {
			m.history.WriteString(systemStyle.Render("[session compacted]") + "\n\n")
		}
		m.historyPrefix = m.history.String()
		m.viewport.SetContent(m.history.String())
		m.viewport.GotoBottom()
		m.textarea.Focus()
		return m, nil
	}

	if !m.streaming {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)

		// Update completion state after textarea content changes.
		m.updateCompletions()
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}
