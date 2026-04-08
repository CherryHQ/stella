package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

func (m chatModel) handlePickingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit

	case tea.KeyEsc:
		m.picking = false
		m.viewport.SetContent(m.history.String())
		m.viewport.GotoBottom()
		m.textarea.Focus()
		return m, nil

	case tea.KeyUp:
		if m.modelCursor > 0 {
			m.modelCursor--
		}
		m.viewport.SetContent(renderModelPicker(m.filteredModels, m.modelCursor, m.provider, m.model, m.modelFilter))
		return m, nil

	case tea.KeyDown:
		if m.modelCursor < len(m.filteredModels)-1 {
			m.modelCursor++
		}
		m.viewport.SetContent(renderModelPicker(m.filteredModels, m.modelCursor, m.provider, m.model, m.modelFilter))
		return m, nil

	case tea.KeyEnter:
		if len(m.filteredModels) == 0 {
			return m, nil
		}
		selected := m.filteredModels[m.modelCursor]
		m.picking = false

		if selected.provider == m.provider && selected.model == m.model {
			m.viewport.SetContent(m.history.String())
			m.viewport.GotoBottom()
			m.textarea.Focus()
			return m, nil
		}

		if m.switchModel != nil {
			if err := m.switchModel(selected.provider, selected.model); err != nil {
				m.history.WriteString(errorStyle.Render("error switching model: "+err.Error()) + "\n\n")
				m.viewport.SetContent(m.history.String())
				m.viewport.GotoBottom()
				m.textarea.Focus()
				return m, nil
			}
		}

		info, err := m.pool.RotateSession(cliChannel)
		if err != nil {
			m.history.WriteString(errorStyle.Render("error creating session: "+err.Error()) + "\n\n")
		} else {
			m.sessionID = info.ID
		}

		m.provider = selected.provider
		m.model = selected.model
		m.history.WriteString(systemStyle.Render(fmt.Sprintf("[switched to %s/%s]", m.provider, m.model)) + "\n\n")
		m.viewport.SetContent(m.history.String())
		m.viewport.GotoBottom()
		m.textarea.Focus()
		return m, nil

	case tea.KeyBackspace:
		if len(m.modelFilter) > 0 {
			m.modelFilter = m.modelFilter[:len(m.modelFilter)-1]
			m.filteredModels = filterModels(m.models, m.modelFilter)
			m.modelCursor = 0
			m.viewport.SetContent(renderModelPicker(m.filteredModels, m.modelCursor, m.provider, m.model, m.modelFilter))
			m.viewport.GotoTop()
		}
		return m, nil

	case tea.KeyRunes:
		m.modelFilter += string(msg.Runes)
		m.filteredModels = filterModels(m.models, m.modelFilter)
		m.modelCursor = 0
		m.viewport.SetContent(renderModelPicker(m.filteredModels, m.modelCursor, m.provider, m.model, m.modelFilter))
		m.viewport.GotoTop()
		return m, nil
	}

	return m, nil
}

func (m chatModel) currentModelIndex() int {
	for i, opt := range m.filteredModels {
		if opt.provider == m.provider && opt.model == m.model {
			return i
		}
	}
	return 0
}
