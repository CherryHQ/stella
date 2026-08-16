package feishucard

import (
	"regexp"
	"strings"
)

// detailsRegex matches a <details>...</details> section. The match is
// non-greedy, so a nested <details> closes on the inner tag; nesting deeper
// than that renders as plain text.
var detailsRegex = regexp.MustCompile(`(?is)<details(\s[^>]*)?>(.*?)</details>`)

// summaryRegex matches a leading <summary>...</summary> inside a details body.
var summaryRegex = regexp.MustCompile(`(?is)^\s*<summary(?:\s[^>]*)?>(.*?)</summary>`)

// defaultSummary is the panel header used when a <details> has no <summary>.
const defaultSummary = "详情"

// renderDetails splits source on <details> sections, converting each into a
// Feishu collapsible panel and rendering the surrounding markdown normally.
// Reports false when there is nothing to convert, so the caller can take the
// plain path.
func (st *renderState) renderDetails(source string) ([]map[string]any, bool) {
	locs := detailsRegex.FindAllStringSubmatchIndex(source, -1)
	if len(locs) == 0 {
		return nil, false
	}

	var elements []map[string]any
	prev := 0
	converted := false

	for _, loc := range locs {
		// A <details> inside a code fence or code span is sample text.
		if inCode(source, loc[0]) {
			continue
		}
		if before := strings.TrimSpace(source[prev:loc[0]]); before != "" {
			elements = append(elements, st.renderBlocks(before)...)
		}
		attrs := ""
		if loc[2] >= 0 {
			attrs = source[loc[2]:loc[3]]
		}
		elements = append(elements, st.buildCollapsiblePanel(attrs, source[loc[4]:loc[5]]))
		prev = loc[1]
		converted = true
	}

	if !converted {
		return nil, false
	}
	if after := strings.TrimSpace(source[prev:]); after != "" {
		elements = append(elements, st.renderBlocks(after)...)
	}
	return elements, true
}

// buildCollapsiblePanel converts one <details> body into a collapsible_panel
// component, using <summary> as the header title.
func (st *renderState) buildCollapsiblePanel(attrs, body string) map[string]any {
	title := defaultSummary
	if m := summaryRegex.FindStringSubmatchIndex(body); m != nil {
		if t := strings.TrimSpace(body[m[2]:m[3]]); t != "" {
			title = t
		}
		body = body[m[1]:]
	}

	elements := st.render(strings.TrimSpace(body))
	if len(elements) == 0 {
		elements = []map[string]any{{"tag": "markdown", "content": " "}}
	}

	return map[string]any{
		"tag":      "collapsible_panel",
		"expanded": hasOpenAttr(attrs),
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

// hasOpenAttr reports whether the <details> tag carries the `open` attribute.
func hasOpenAttr(attrs string) bool {
	for f := range strings.FieldsSeq(strings.ToLower(attrs)) {
		if f == "open" || strings.HasPrefix(f, "open=") {
			return true
		}
	}
	return false
}
