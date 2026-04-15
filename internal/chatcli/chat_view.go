package cli

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// horizontal padding on each side of the content area
const padX = 1

func (m *chatModel) resize() {
	// Layout height budget (count actual rendered lines):
	//   header (title):             1 line
	//   blank line (\n\n):          1 line
	//   viewport:                   vpHeight lines
	//   input separator top:        1 line
	//   prompt + textarea:          ta.Height lines
	//   input separator bottom:     1 line
	//   help bar:                   1 line
	//   completion popup:           variable
	const chrome = 1 + 1 + 1 + 1 + 1 + 1 // header + blank + sep top + sep bottom + newline + help

	completionHeight := 0
	if m.completing && len(m.completions) > 0 {
		completionHeight = len(m.completions)
	}

	vpHeight := max(m.height-chrome-m.textarea.Height()-completionHeight, 1)

	innerWidth := m.width - padX*2

	if !m.ready {
		m.viewport = viewport.New(innerWidth, vpHeight)
		m.viewport.SetContent(m.history.String())
		m.ready = true
	} else {
		m.viewport.Width = innerWidth
		m.viewport.Height = vpHeight
	}

	m.textarea.SetWidth(innerWidth - 2) // subtract prompt "> " width

	// Recreate markdown renderer with no document margin for flush-left alignment
	style := styles.DarkStyleConfig
	if !termenv.HasDarkBackground() {
		style = styles.LightStyleConfig
	}
	style.Document.Margin = uintPtr(0)
	style.Document.BlockPrefix = ""
	style.Document.BlockSuffix = ""
	m.mdRenderer, _ = glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(innerWidth),
	)
}

// renderMarkdown renders raw markdown text using glamour, falling back to raw text on error.
func (m *chatModel) renderMarkdown(raw string) string {
	if m.mdRenderer == nil || raw == "" {
		return raw
	}
	rendered, err := m.mdRenderer.Render(raw)
	if err != nil {
		return raw
	}
	return strings.TrimRight(rendered, "\n")
}

// refreshViewport rebuilds viewport content from historyPrefix + rendered current response.
func (m *chatModel) refreshViewport() {
	rendered := m.renderMarkdown(m.currentRaw.String())
	content := m.historyPrefix + agentBorderStyle.Render(rendered)
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
}

func uintPtr(v uint) *uint { return &v }

// padLines prepends padding to each line of text.
func padLines(text, pad string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = pad + line
	}
	return strings.Join(lines, "\n")
}

func (m chatModel) View() string {
	if !m.ready {
		return "Initializing..."
	}

	pad := strings.Repeat(" ", padX)

	// Title bar: "Anna" left, "provider/model" right
	title := titleStyle.Render("Anna")
	modelInfo := modelInfoStyle.Render(m.provider + "/" + m.model)
	titleGap := max(m.width-padX*2-lipgloss.Width(title)-lipgloss.Width(modelInfo), 0)
	header := pad + title + strings.Repeat(" ", titleGap) + modelInfo + pad

	// Input area — two thin separator lines with > prompt
	sepLine := pad + inputSeparator.Render(strings.Repeat("─", m.width-padX*2))
	prompt := inputPromptStyle.Render(">")
	input := sepLine + "\n" + pad + prompt + " " + m.textarea.View() + "\n" + sepLine

	// Completion popup
	completionView := ""
	if m.completing && len(m.completions) > 0 {
		completionView = "\n" + renderCompletions(m.completions, m.completeCursor)
	}

	// Help bar below input
	var helpText string
	switch {
	case m.picking:
		helpText = helpStyle.Render("↑↓ · enter · esc")
	case m.completing:
		helpText = helpStyle.Render("↑↓ · tab · enter · esc")
	default:
		helpText = helpAccentStyle.Render("/help") + helpStyle.Render(" · ") +
			helpAccentStyle.Render("/new") + helpStyle.Render(" · ") +
			helpAccentStyle.Render("/model") + helpStyle.Render(" · ") +
			helpAccentStyle.Render("/quit")
	}
	status := ""
	if m.status != "" {
		status = statusStyle.Render(m.status)
	}
	helpGap := max(m.width-padX*2-lipgloss.Width(helpText)-lipgloss.Width(status), 0)
	helpBar := pad + helpText + strings.Repeat(" ", helpGap) + status

	// Viewport with padding
	vpView := padLines(m.viewport.View(), pad)

	return header + "\n\n" + vpView + "\n" + input + completionView + "\n" + helpBar
}
