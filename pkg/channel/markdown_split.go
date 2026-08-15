package channel

import (
	"strings"
	"unicode/utf8"
)

// fenceState tracks an open ``` or ~~~ code fence while SplitMarkdown walks
// lines, so a chunk boundary that falls inside a fence can close it and the
// next chunk can reopen it with the same marker and info string.
type fenceState struct {
	active bool
	marker byte // '`' or '~'
	length int
	info   string
}

func (f fenceState) closeLine() string {
	return strings.Repeat(string(f.marker), f.length) + "\n"
}

func (f fenceState) openLine() string {
	line := strings.Repeat(string(f.marker), f.length)
	if f.info != "" {
		line += f.info
	}
	return line + "\n"
}

// SplitMarkdown splits text into chunks of at most maxRunes Unicode runes,
// preferring to break at line boundaries. If a break falls inside a ``` or
// ~~~ code fence, the fence is closed at the end of the chunk and reopened
// with the same marker and info string at the start of the next chunk, so
// every chunk is independently valid Markdown.
func SplitMarkdown(text string, maxRunes int) []string {
	if maxRunes < 1 {
		maxRunes = 1
	}
	if utf8.RuneCountInString(text) <= maxRunes {
		return []string{text}
	}

	var chunks []string
	var b strings.Builder
	bRunes := 0
	var fence fenceState

	// closeOverhead is the worst-case room a flush needs to reserve for the
	// fence close: the close line itself, plus a leading "\n" flush adds
	// whenever the chunk being closed does not already end in one (always
	// true for a chunk boundary that lands mid-line, e.g. an overlong single
	// line). Reserving it unconditionally is sometimes one rune more than a
	// line-boundary flush actually needs, but never less than any flush
	// needs, so no chunk can ever exceed maxRunes.
	closeOverhead := func() int {
		if !fence.active {
			return 0
		}
		return utf8.RuneCountInString(fence.closeLine()) + 1
	}

	flush := func() {
		chunk := b.String()
		if fence.active {
			if !strings.HasSuffix(chunk, "\n") {
				chunk += "\n"
			}
			chunk += fence.closeLine()
		}
		chunks = append(chunks, chunk)
		b.Reset()
		bRunes = 0
		if fence.active {
			open := fence.openLine()
			b.WriteString(open)
			bRunes = utf8.RuneCountInString(open)
		}
	}

	// appendRunes writes s into the current chunk, flushing (and reserving
	// room for a fence close/reopen) as needed when s alone overruns maxRunes.
	appendRunes := func(s string) {
		for utf8.RuneCountInString(s) > 0 {
			avail := maxRunes - bRunes - closeOverhead()
			if avail <= 0 {
				flush()
				avail = maxRunes - bRunes - closeOverhead()
				if avail <= 0 {
					avail = 1 // maxRunes too small for overhead; still make progress
				}
			}
			sRunes := utf8.RuneCountInString(s)
			if sRunes <= avail {
				b.WriteString(s)
				bRunes += sRunes
				return
			}
			head, tail := splitAtRune(s, avail)
			b.WriteString(head)
			bRunes += avail
			s = tail
			flush()
		}
	}

	for _, line := range splitLinesKeepEnds(text) {
		trimmed := strings.TrimRight(line, "\n")
		marker, length, info, isFence := detectFence(trimmed)
		willClose := fence.active && isFence && marker == fence.marker && length >= fence.length
		willOpen := !fence.active && isFence

		lineRunes := utf8.RuneCountInString(line)
		overhead := 0
		if fence.active && !willClose {
			overhead = closeOverhead()
		}
		if bRunes > 0 && bRunes+lineRunes+overhead > maxRunes {
			flush()
		}

		appendRunes(line)

		if willOpen {
			fence = fenceState{active: true, marker: marker, length: length, info: info}
		} else if willClose {
			fence.active = false
		}
	}

	if bRunes > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

// detectFence reports whether line (without its trailing newline) opens or
// closes a Markdown code fence: three or more of the same backtick or tilde
// character, optionally indented up to 3 spaces, optionally followed by an
// info string. Backtick fences cannot carry a backtick in the info string
// (CommonMark), so such lines are treated as ordinary content.
func detectFence(line string) (marker byte, length int, info string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return 0, 0, "", false
	}
	ch := trimmed[0]
	if ch != '`' && ch != '~' {
		return 0, 0, "", false
	}
	i := 0
	for i < len(trimmed) && trimmed[i] == ch {
		i++
	}
	if i < 3 {
		return 0, 0, "", false
	}
	rest := strings.TrimSpace(trimmed[i:])
	if ch == '`' && strings.ContainsRune(rest, '`') {
		return 0, 0, "", false
	}
	return ch, i, rest, true
}

// splitLinesKeepEnds splits s into lines, keeping the trailing "\n" on every
// line except possibly the last.
func splitLinesKeepEnds(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// splitAtRune splits s after its first n runes without cutting a rune in
// half.
func splitAtRune(s string, n int) (head, tail string) {
	if n <= 0 {
		return "", s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i], s[i:]
		}
		count++
	}
	return s, ""
}
