package feishucard

import (
	"strings"

	"github.com/CherryHQ/stella/pkg/goldmark/mdutil"
)

// defaultSummary is the panel header used when a <details> has no <summary>.
const defaultSummary = "详情"

// renderDetails splits source on <details> sections, converting each into a
// Feishu collapsible panel and rendering the surrounding markdown normally.
// Reports false when there is nothing to convert, so the caller can take the
// plain path.
func (st *renderState) renderDetails(source string) ([]map[string]any, bool) {
	sections := mdutil.FindDetails(source)
	if len(sections) == 0 {
		return nil, false
	}

	var elements []map[string]any
	prev := 0
	for _, d := range sections {
		if before := strings.TrimSpace(source[prev:d.Start]); before != "" {
			elements = append(elements, st.renderBlocks(before)...)
		}
		elements = append(elements, st.buildCollapsiblePanel(d))
		prev = d.End
	}
	if after := strings.TrimSpace(source[prev:]); after != "" {
		elements = append(elements, st.renderBlocks(after)...)
	}
	return elements, true
}

// buildCollapsiblePanel converts one <details> section into a
// collapsible_panel component, using <summary> as the header title.
func (st *renderState) buildCollapsiblePanel(d mdutil.Details) map[string]any {
	title := d.Summary
	if title == "" {
		title = defaultSummary
	}

	elements := st.render(d.Body)
	if len(elements) == 0 {
		elements = []map[string]any{{"tag": "markdown", "content": " "}}
	}

	return map[string]any{
		"tag":      "collapsible_panel",
		"expanded": d.Open,
		"header": map[string]any{
			"title":          map[string]any{"tag": "markdown", "content": title},
			"vertical_align": "center",
			"icon": map[string]any{
				"tag":   "standard_icon",
				"token": "down-small-ccm_outlined",
				"color": "grey",
				"size":  "16px 16px",
			},
			"icon_position":       "right",
			"icon_expanded_angle": -180,
		},
		"border": map[string]any{
			"color":         "grey",
			"corner_radius": "5px",
		},
		"vertical_spacing": "8px",
		"padding":          "8px 8px 8px 8px",
		"elements":         elements,
	}
}
