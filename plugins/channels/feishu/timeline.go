package feishu

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/hooks"
)

const maxTimelinePanelBytes = 2_500

type timelineEntryKind int

const (
	timelineReasoning timelineEntryKind = iota
	timelineTool
)

type timelineEntry struct {
	kind     timelineEntryKind
	id       string
	text     string
	tool     string
	input    string
	status   string
	started  time.Time
	duration time.Duration
}

// streamTimeline keeps the platform presentation independent from raw tool
// payloads. Arguments, results, and detail never enter the card.
type streamTimeline struct {
	entries []timelineEntry
}

func (t *streamTimeline) addReasoning(delta string) {
	if delta == "" {
		return
	}
	if len(t.entries) > 0 && t.entries[len(t.entries)-1].kind == timelineReasoning {
		t.entries[len(t.entries)-1].text += delta
		return
	}
	t.entries = append(t.entries, timelineEntry{kind: timelineReasoning, text: delta})
}

func (t *streamTimeline) handleTool(event *channel.ToolUseEvent) {
	if event == nil {
		return
	}
	if event.Status == "running" {
		t.entries = append(t.entries, timelineEntry{
			kind:    timelineTool,
			id:      event.ID,
			tool:    event.Tool,
			input:   event.Input,
			status:  event.Status,
			started: nowFunc(),
		})
		return
	}

	for i := len(t.entries) - 1; i >= 0; i-- {
		entry := &t.entries[i]
		if entry.kind != timelineTool || entry.status != "running" {
			continue
		}
		if event.ID != "" && entry.id != event.ID {
			continue
		}
		if event.ID == "" && entry.tool != event.Tool {
			continue
		}
		entry.status = event.Status
		entry.duration = max(nowFunc().Sub(entry.started), 0)
		if event.Input != "" {
			entry.input = event.Input
		}
		return
	}

	t.entries = append(t.entries, timelineEntry{
		kind:   timelineTool,
		id:     event.ID,
		tool:   event.Tool,
		input:  event.Input,
		status: event.Status,
	})
}

func (t *streamTimeline) markdown(expanded bool) string {
	panels := t.panels(expanded)
	return strings.Join(panels, "\n\n")
}

func (t *streamTimeline) latestMarkdown() string {
	panels := t.panels(true)
	if len(panels) == 0 {
		return ""
	}
	return panels[len(panels)-1]
}

func (t *streamTimeline) panels(expanded bool) []string {
	rendered := make([]string, 0, len(t.entries))
	toolCount := 0
	for _, entry := range t.entries {
		if entry.kind == timelineTool {
			toolCount++
		}
		rendered = append(rendered, renderTimelineEntry(entry)...)
	}
	if len(rendered) == 0 {
		return nil
	}

	groups := make([]string, 0, 1)
	current := ""
	for _, entry := range rendered {
		candidate := entry
		if current != "" {
			candidate = current + "\n\n" + entry
		}
		if len(candidate) <= maxTimelinePanelBytes {
			current = candidate
			continue
		}
		if current != "" {
			groups = append(groups, current)
		}
		current = entry
	}
	if current != "" {
		groups = append(groups, current)
	}

	panels := make([]string, 0, len(groups))
	for i, body := range groups {
		summary := "思考与工具"
		if toolCount > 0 {
			summary += fmt.Sprintf(" · %d 个工具", toolCount)
		}
		if len(groups) > 1 {
			summary += fmt.Sprintf(" · %d/%d", i+1, len(groups))
		}
		open := ""
		if expanded && i == len(groups)-1 {
			open = " open"
		}
		panels = append(panels, fmt.Sprintf("<details%s>\n<summary>%s</summary>\n\n%s\n\n</details>", open, summary, body))
	}
	return panels
}

func renderTimelineEntry(entry timelineEntry) []string {
	if entry.kind == timelineReasoning {
		text := html.EscapeString(hooks.RedactToolText(entry.text))
		parts := channel.SplitMessage(text, maxTimelinePanelBytes-64)
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			out = append(out, "💭 **思考**\n\n"+part)
		}
		return out
	}

	status := "⏳"
	switch entry.status {
	case "done":
		status = "✅"
	case "error":
		status = "❌"
	}
	line := fmt.Sprintf("%s %s **%s**", status, channel.EmojiFor(entry.tool), html.EscapeString(entry.tool))
	if entry.duration > 0 {
		line += " · " + channel.FormatDuration(entry.duration)
	}
	if input := timelineInput(entry.input); input != "" {
		line += "\n\n> " + input
	}
	return []string{line}
}

func timelineInput(input string) string {
	input = hooks.RedactToolText(input)
	input = strings.Join(strings.Fields(input), " ")
	return html.EscapeString(channel.Truncate(input, 160))
}
