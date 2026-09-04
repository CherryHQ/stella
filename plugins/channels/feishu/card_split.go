package feishu

import (
	"strings"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/goldmark/mdutil"
)

// splitCardText keeps Markdown blocks intact while packing them into cards that
// satisfy both the message text ceiling and the fully rendered card budget.
func splitCardText(text string, status cardStatus) []string {
	if text == "" {
		return []string{""}
	}

	blocks := markdownBlocks(text)
	chunks := make([]string, 0, len(blocks))
	current := ""
	flush := func() {
		if current == "" {
			return
		}
		chunks = append(chunks, current)
		current = ""
	}

	for _, block := range blocks {
		parts := splitOversizeMarkdownBlock(block)
		for _, part := range parts {
			candidate := part
			if current != "" {
				candidate = current + "\n\n" + part
			}
			if cardChunkFits(candidate, status) {
				current = candidate
				continue
			}

			flush()
			if cardChunkFits(part, status) {
				current = part
				continue
			}

			// A single pathological block can still exceed the rendered JSON
			// budget. Preserve UTF-8 and prefer a complete answer over a rejected
			// card; the caller will fall back to plain text if a fragment remains
			// unrenderable.
			for _, fragment := range channel.SplitMessage(part, feishuMaxMessageLen/2) {
				if current != "" {
					flush()
				}
				current = fragment
			}
		}
	}
	flush()
	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

func cardChunkFits(text string, status cardStatus) bool {
	if len(text) > feishuMaxMessageLen {
		return false
	}
	_, err := buildCardContentForStatus(text, status)
	return err == nil
}

func markdownBlocks(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	blocks := make([]string, 0, len(lines))
	var block strings.Builder
	fence := ""
	inDetails := false
	flush := func() {
		value := strings.Trim(block.String(), "\n")
		if value != "" {
			blocks = append(blocks, value)
		}
		block.Reset()
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if fence == "" && !inDetails && trimmed == "" {
			flush()
			continue
		}
		block.WriteString(line)
		if fence == "" {
			lower := strings.ToLower(trimmed)
			if strings.HasPrefix(lower, "<details") {
				inDetails = true
			} else if lower == "</details>" {
				inDetails = false
			}
		}
		if marker := markdownFenceMarker(trimmed); marker != "" {
			if fence == "" {
				fence = marker
			} else if marker == fence {
				fence = ""
			}
		}
	}
	flush()
	return blocks
}

func markdownFenceMarker(line string) string {
	switch {
	case strings.HasPrefix(line, "```"):
		return "```"
	case strings.HasPrefix(line, "~~~"):
		return "~~~"
	default:
		return ""
	}
}

func splitOversizeMarkdownBlock(block string) []string {
	if len(block) <= feishuMaxMessageLen {
		return []string{block}
	}
	if chunks := splitDetailsBlock(block); len(chunks) > 0 {
		return chunks
	}
	if chunks := splitFencedCodeBlock(block); len(chunks) > 0 {
		return chunks
	}
	if chunks := splitMarkdownTable(block); len(chunks) > 0 {
		return chunks
	}
	return channel.SplitMessage(block, feishuMaxMessageLen)
}

func splitDetailsBlock(block string) []string {
	details := mdutil.FindDetails(block)
	if len(details) != 1 || details[0].Start != 0 || details[0].End != len(block) {
		return nil
	}
	detail := details[0]
	opener := "<details>"
	if detail.Open {
		opener = "<details open>"
	}
	prefix := opener + "\n"
	if detail.Summary != "" {
		prefix += "<summary>" + detail.Summary + "</summary>\n\n"
	}
	suffix := "\n\n</details>"
	maxBody := feishuMaxMessageLen - len(prefix) - len(suffix)
	if maxBody <= 0 {
		return nil
	}
	bodyParts := channel.SplitMessage(detail.Body, maxBody)
	chunks := make([]string, 0, len(bodyParts))
	for _, body := range bodyParts {
		chunks = append(chunks, prefix+strings.Trim(body, "\n")+suffix)
	}
	return chunks
}

func splitFencedCodeBlock(block string) []string {
	firstBreak := strings.IndexByte(block, '\n')
	lastBreak := strings.LastIndexByte(block, '\n')
	if firstBreak < 0 || lastBreak <= firstBreak {
		return nil
	}
	opener := block[:firstBreak]
	marker := markdownFenceMarker(strings.TrimSpace(opener))
	closer := strings.TrimSpace(block[lastBreak+1:])
	if marker == "" || closer != marker {
		return nil
	}

	maxBody := feishuMaxMessageLen - len(opener) - len(marker) - 2
	if maxBody <= 0 {
		return nil
	}
	body := block[firstBreak+1 : lastBreak]
	parts := channel.SplitMessage(body, maxBody)
	chunks := make([]string, 0, len(parts))
	for _, part := range parts {
		chunks = append(chunks, opener+"\n"+part+"\n"+marker)
	}
	return chunks
}

func splitMarkdownTable(block string) []string {
	lines := strings.Split(block, "\n")
	if len(lines) < 3 || !looksLikeTableSeparator(lines[1]) {
		return nil
	}
	header := lines[0] + "\n" + lines[1]
	chunks := make([]string, 0, len(lines)/2)
	current := header
	for _, row := range lines[2:] {
		candidate := current + "\n" + row
		if len(candidate) <= feishuMaxMessageLen {
			current = candidate
			continue
		}
		chunks = append(chunks, current)
		current = header + "\n" + row
	}
	chunks = append(chunks, current)
	return chunks
}

func looksLikeTableSeparator(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "-") || !strings.Contains(line, "|") {
		return false
	}
	for _, r := range line {
		if r != '|' && r != '-' && r != ':' && r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}
