package feishucard

import (
	"regexp"
	"strings"

	"github.com/CherryHQ/stella/pkg/goldmark/mdutil"
)

// buttonRegex matches {{button key="value" ...}} patterns.
// Attributes are key="value" pairs inside the braces.
var buttonRegex = regexp.MustCompile(`\{\{button\s+([^}]*)\}\}`)

// attrRegex extracts key="value" pairs from the attribute string.
var attrRegex = regexp.MustCompile(`(\w+)="([^"]*)"`)

// extractButtons scans a markdown string for {{button ...}} patterns and
// returns a list of elements: markdown segments interleaved with button
// elements. Consecutive buttons are grouped into a column_set.
// If no buttons are found, returns nil (caller keeps the original element).
func extractButtons(content string) []map[string]any {
	matches := buttonRegex.FindAllStringIndex(content, -1)
	if len(matches) == 0 {
		return nil
	}

	var elements []map[string]any
	prev := 0
	converted := false

	for _, loc := range matches {
		if mdutil.InCode(content, loc[0]) {
			continue
		}

		raw := content[loc[0]:loc[1]]
		btn := parseButton(raw)
		if btn == nil {
			continue
		}

		// Text before this button. Invalid directives between prev and loc stay
		// in this markdown segment instead of disappearing.
		before := strings.TrimRight(content[prev:loc[0]], "\n ")
		if before != "" {
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": before,
			})
		}

		elements = append(elements, btn)
		converted = true
		prev = loc[1]
	}

	if !converted {
		return nil
	}

	// Text after the last converted button.
	after := strings.TrimLeft(content[prev:], "\n ")
	if after != "" {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": after,
		})
	}

	return groupButtons(elements)
}

// parseButton extracts attributes from a {{button ...}} match and builds
// a Feishu button element. Returns nil if required attributes are missing.
// A button is either a link (url=) that opens a URL or a callback (value=)
// that posts an action back to the bot; url takes precedence when both are set.
func parseButton(raw string) map[string]any {
	sub := buttonRegex.FindStringSubmatch(raw)
	if len(sub) < 2 {
		return nil
	}

	attrs := parseAttrs(sub[1])
	label := attrs["label"]
	url := attrs["url"]
	value := attrs["value"]
	if label == "" || (url == "" && value == "") {
		return nil
	}

	return buildButtonElement(label, url, value, attrs["type"], attrs["confirm"])
}

// parseAttrs extracts key="value" pairs from an attribute string.
func parseAttrs(s string) map[string]string {
	m := make(map[string]string)
	for _, match := range attrRegex.FindAllStringSubmatch(s, -1) {
		m[match[1]] = match[2]
	}
	return m
}

// buildButtonElement constructs a Feishu button component. When url is
// non-empty the button opens that URL; otherwise it posts value back as a
// callback action.
func buildButtonElement(label, url, value, typ, confirm string) map[string]any {
	if typ == "" {
		typ = "default"
	}

	behavior := map[string]any{"type": "callback", "value": map[string]any{"action": value}}
	if url != "" {
		behavior = map[string]any{"type": "open_url", "default_url": url}
	}

	btn := map[string]any{
		"tag":  "button",
		"type": typ,
		"text": map[string]any{
			"tag":     "plain_text",
			"content": label,
		},
		"behaviors": []map[string]any{behavior},
	}

	if confirm != "" {
		btn["confirm"] = map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": "确认",
			},
			"text": map[string]any{
				"tag":     "plain_text",
				"content": confirm,
			},
		}
	}

	return btn
}

// groupButtons wraps consecutive button elements in a column_set for
// horizontal layout. Solo buttons remain standalone.
func groupButtons(elements []map[string]any) []map[string]any {
	var result []map[string]any
	var buttonRun []map[string]any

	flushRun := func() {
		if len(buttonRun) == 0 {
			return
		}
		if len(buttonRun) == 1 {
			result = append(result, buttonRun[0])
		} else {
			columns := make([]map[string]any, len(buttonRun))
			for i, btn := range buttonRun {
				columns[i] = map[string]any{
					"tag":            "column",
					"width":          "auto",
					"vertical_align": "center",
					"elements":       []map[string]any{btn},
				}
			}
			result = append(result, map[string]any{
				"tag":     "column_set",
				"columns": columns,
			})
		}
		buttonRun = nil
	}

	for _, elem := range elements {
		if elem["tag"] == "button" {
			buttonRun = append(buttonRun, elem)
		} else {
			flushRun()
			result = append(result, elem)
		}
	}
	flushRun()

	return result
}
