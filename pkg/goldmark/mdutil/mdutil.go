// Package mdutil holds small markdown source scanners shared by the channel
// renderers. Each channel maps the results onto its own output format.
package mdutil

import (
	"regexp"
	"strings"
)

// detailsRegex matches a <details>...</details> section. The match is
// non-greedy, so a nested <details> closes on the inner tag; nesting deeper
// than that is left to the caller as plain text.
var detailsRegex = regexp.MustCompile(`(?is)<details(\s[^>]*)?>(.*?)</details>`)

// summaryRegex matches a leading <summary>...</summary> inside a details body.
var summaryRegex = regexp.MustCompile(`(?is)^\s*<summary(?:\s[^>]*)?>(.*?)</summary>`)

// autoLinkRegex matches an <https://…> or <name@example.com> autolink. The
// scheme list is deliberately narrow so that HTML tags are not mistaken for
// links.
var autoLinkRegex = regexp.MustCompile(`<((?:https?|ftp)://[^\s<>]+|[^\s<>@]+@[^\s<>@]+\.[^\s<>@]+)>`)

// Details is one <details> section located in a markdown source.
type Details struct {
	Start, End int    // byte range of the whole section in the source
	Open       bool   // the tag carried the `open` attribute
	Summary    string // <summary> text, empty when the section has none
	Body       string // section content with the <summary> removed
}

// FindDetails returns the <details> sections of text in source order,
// skipping any that sit inside a code fence or code span.
func FindDetails(text string) []Details {
	var found []Details
	for _, loc := range detailsRegex.FindAllStringSubmatchIndex(text, -1) {
		if InCode(text, loc[0]) {
			continue
		}
		d := Details{Start: loc[0], End: loc[1], Body: text[loc[4]:loc[5]]}
		if loc[2] >= 0 {
			d.Open = hasOpenAttr(text[loc[2]:loc[3]])
		}
		if m := summaryRegex.FindStringSubmatchIndex(d.Body); m != nil {
			d.Summary = strings.TrimSpace(d.Body[m[2]:m[3]])
			d.Body = d.Body[m[1]:]
		}
		d.Body = strings.TrimSpace(d.Body)
		found = append(found, d)
	}
	return found
}

// ExpandAutoLinks rewrites <https://…> autolinks into [url](url) form, leaving
// code fences and code spans untouched. Renderers that drop goldmark's
// AutoLink node would otherwise lose the link entirely.
func ExpandAutoLinks(text string) string {
	locs := autoLinkRegex.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return text
	}

	var b strings.Builder
	prev := 0
	for _, loc := range locs {
		if InCode(text, loc[0]) {
			continue
		}
		target := text[loc[2]:loc[3]]
		b.WriteString(text[prev:loc[0]])
		b.WriteString("[" + target + "](" + mailtoIfEmail(target) + ")")
		prev = loc[1]
	}
	b.WriteString(text[prev:])
	return b.String()
}

func mailtoIfEmail(target string) string {
	if strings.Contains(target, "://") {
		return target
	}
	return "mailto:" + target
}

// InCode reports whether pos falls inside a fenced code block or code span.
func InCode(text string, pos int) bool {
	inFence := false
	lineStart := 0
	for lineStart < len(text) {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd == -1 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}

		line := text[lineStart:lineEnd]
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") {
			if pos >= lineStart && pos < lineEnd {
				return true
			}
			inFence = !inFence
		} else if pos >= lineStart && pos < lineEnd {
			return inFence || strings.Count(text[lineStart:pos], "`")%2 == 1
		}

		lineStart = lineEnd + 1
	}
	return false
}

// hasOpenAttr reports whether a <details> attribute string carries `open`.
func hasOpenAttr(attrs string) bool {
	for f := range strings.FieldsSeq(strings.ToLower(attrs)) {
		if f == "open" || strings.HasPrefix(f, "open=") {
			return true
		}
	}
	return false
}
