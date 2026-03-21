package cli

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/vaayne/anna/internal/agent/runner"
)

func (m *chatModel) handleInput(input string) tea.Cmd {
	switch input {
	case "/quit", "/exit":
		return tea.Quit
	case "/new":
		info, err := m.pool.RotateSession(cliChannel)
		if err != nil {
			m.history.WriteString(errorStyle.Render("error: "+err.Error()) + "\n\n")
		} else {
			m.sessionID = info.ID
			m.history.Reset()
			m.historyPrefix = ""
			m.history.WriteString(systemStyle.Render("[new session started]") + "\n\n")
		}
		m.viewport.SetContent(m.history.String())
		m.viewport.GotoBottom()
		return nil
	case "/compact":
		m.streaming = true
		m.status = "Compacting session..."
		m.textarea.Blur()
		ctx := m.ctx
		sessionID := m.sessionID
		return func() tea.Msg {
			summary, err := m.pool.CompactSession(ctx, sessionID)
			return compactDoneMsg{summary: summary, err: err}
		}
	case "/whoami":
		userInfo := fmt.Sprintf("Channel: %s\n\nCLI runs locally — use /whoami in Telegram, QQ, or Feishu to get your user ID for notifications.", cliChannel)
		m.history.WriteString(systemStyle.Render(userInfo) + "\n\n")
		m.viewport.SetContent(m.history.String())
		m.viewport.GotoBottom()
		return nil
	case "/agent":
		m.history.WriteString(systemStyle.Render("Agent switching is available in Telegram, QQ, and Feishu channels.\nUse /agent in those channels to list or switch agents.") + "\n\n")
		m.viewport.SetContent(m.history.String())
		m.viewport.GotoBottom()
		return nil
	case "/model":
		m.models = toModelOptions(m.listModels())
		if len(m.models) == 0 {
			m.history.WriteString(systemStyle.Render("[no models configured]") + "\n\n")
			m.viewport.SetContent(m.history.String())
			m.viewport.GotoBottom()
			return nil
		}
		m.picking = true
		m.modelFilter = ""
		m.filteredModels = m.models
		m.modelCursor = m.currentModelIndex()
		m.textarea.Blur()
		m.viewport.SetContent(renderModelPicker(m.filteredModels, m.modelCursor, m.provider, m.model, m.modelFilter))
		m.viewport.GotoTop()
		return nil
	}

	m.history.WriteString(userStyle.Render("You") + "\n" + userBorderStyle.Render(chatTextStyle.Render(input)) + "\n\n")
	m.history.WriteString(agentStyle.Render("Anna") + "\n")
	m.historyPrefix = m.history.String()
	m.currentRaw.Reset()
	m.viewport.SetContent(m.historyPrefix)
	m.viewport.GotoBottom()

	m.streaming = true
	m.status = "Thinking..."
	m.textarea.Blur()

	ctx := m.ctx
	sessionID := m.sessionID
	return func() tea.Msg {
		stream := m.pool.Chat(ctx, sessionID, input)
		return streamStartMsg{stream: stream}
	}
}

// waitNextChunk returns a Cmd that reads the next event from the stream channel.
func waitNextChunk(stream <-chan runner.Event) tea.Cmd {
	if stream == nil {
		return nil
	}
	return func() tea.Msg {
		evt, ok := <-stream
		if !ok {
			return streamDoneMsg{}
		}
		if evt.Err != nil {
			return streamErrMsg{evt.Err}
		}
		if evt.ToolUse != nil {
			return streamToolMsg{
				tool:   evt.ToolUse.Tool,
				status: evt.ToolUse.Status,
				input:  evt.ToolUse.Input,
				detail: evt.ToolUse.Detail,
			}
		}
		return streamChunkMsg(evt.Text)
	}
}

// updateCompletions shows/hides the command completion popup based on textarea content.
func (m *chatModel) updateCompletions() {
	val := m.textarea.Value()
	if strings.HasPrefix(val, "/") {
		matches := filterCommands(val)
		wasCompleting := m.completing
		m.completing = len(matches) > 0
		m.completions = matches
		if m.completeCursor >= len(matches) {
			m.completeCursor = 0
		}
		if m.completing != wasCompleting {
			m.resize()
		}
	} else if m.completing {
		m.completing = false
		m.completions = nil
		m.completeCursor = 0
		m.resize()
	}
}
