package channel

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitMarkdown_ShortTextPassesThrough(t *testing.T) {
	got := SplitMarkdown("hello world", 100)
	if len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("unexpected result: %v", got)
	}
}

func TestSplitMarkdown_ExactLength(t *testing.T) {
	text := strings.Repeat("x", 10)
	got := SplitMarkdown(text, 10)
	if len(got) != 1 || got[0] != text {
		t.Fatalf("expected single unchanged chunk, got %v", got)
	}
}

func TestSplitMarkdown_PrefersNewlineBoundaries(t *testing.T) {
	text := "aaaa\nbbbb\ncccc\ndddd"
	chunks := SplitMarkdown(text, 10)
	for _, c := range chunks {
		if utf8.RuneCountInString(c) > 10 {
			t.Fatalf("chunk exceeds maxRunes: %q", c)
		}
	}
	// Every split boundary should land right after a newline, never inside a line.
	for _, c := range chunks {
		if c == "" {
			continue
		}
		if !strings.HasSuffix(c, "\n") && c != chunks[len(chunks)-1] {
			t.Fatalf("chunk does not end on a line boundary: %q", c)
		}
	}
	if strings.Join(chunks, "") != text {
		t.Fatalf("chunks do not reconstruct original text: %v", chunks)
	}
}

func TestSplitMarkdown_CJKTextRespectsRuneBudget(t *testing.T) {
	// Build >2000 runes of Chinese text across many short lines so the
	// byte length (3 bytes/rune) would wildly overshoot a byte-based limit,
	// but the rune-based budget must still be respected.
	line := "这是一行测试文本，用于验证按符文计数的长消息切分逻辑。\n"
	var b strings.Builder
	for utf8.RuneCountInString(b.String()) < 5000 {
		b.WriteString(line)
	}
	text := b.String()

	chunks := SplitMarkdown(text, 2000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for %d-rune text, got %d", utf8.RuneCountInString(text), len(chunks))
	}
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c); n > 2000 {
			t.Fatalf("chunk %d has %d runes, want <= 2000", i, n)
		}
	}
	if strings.Join(chunks, "") != text {
		t.Fatalf("chunks do not reconstruct original CJK text")
	}
}

func TestSplitMarkdown_FenceSpansChunkBoundary(t *testing.T) {
	var body strings.Builder
	for range 200 {
		body.WriteString("line of code that fills space\n")
	}
	text := "before the block\n```go\n" + body.String() + "```\nafter the block\n"

	chunks := SplitMarkdown(text, 300)
	if len(chunks) < 2 {
		t.Fatalf("expected the fence to force multiple chunks, got %d", len(chunks))
	}

	openFences := 0
	for i, c := range chunks {
		fenceLines := 0
		for l := range strings.SplitSeq(c, "\n") {
			if _, _, _, ok := detectFence(l); ok {
				fenceLines++
			}
		}
		if fenceLines%2 != 0 {
			t.Fatalf("chunk %d has unbalanced fence delimiters (%d): %q", i, fenceLines, c)
		}
		if strings.Contains(c, "```go") {
			openFences++
		}
	}
	if openFences < 2 {
		t.Fatalf("expected the fence info string 'go' to reopen in a later chunk, only saw it %d times", openFences)
	}

	// Every chunk that contains fence content must itself be independently
	// valid Markdown: each fence opened is also closed within the chunk.
	for i, c := range chunks {
		if utf8.RuneCountInString(c) > 300 {
			t.Fatalf("chunk %d exceeds maxRunes budget: %d runes", i, utf8.RuneCountInString(c))
		}
	}
}

func TestSplitMarkdown_TildeFenceReopensWithSameMarker(t *testing.T) {
	var body strings.Builder
	for range 200 {
		body.WriteString("data row\n")
	}
	text := "~~~text\n" + body.String() + "~~~\n"

	chunks := SplitMarkdown(text, 200)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks[:len(chunks)-1] {
		if !strings.Contains(c, "~~~") {
			t.Fatalf("chunk %d does not close/reopen with tilde fence: %q", i, c)
		}
	}
	if !strings.Contains(chunks[1], "~~~text") {
		t.Fatalf("expected reopened fence to carry the 'text' info string, got %q", chunks[1])
	}
}

func TestSplitMarkdown_FenceNotCrossingBoundaryIsUntouched(t *testing.T) {
	text := "```js\nconsole.log(1)\n```\n" + strings.Repeat("padding line\n", 50)
	chunks := SplitMarkdown(text, 100)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0], "```js\nconsole.log(1)\n```") {
		t.Fatalf("small fence block should stay intact in the first chunk: %q", chunks[0])
	}
}

func TestSplitMarkdown_OverlongLineInFenceStaysWithinBudget(t *testing.T) {
	cases := []struct {
		name  string
		open  string
		close string
		line  string
	}{
		{"backtick fence, ASCII line", "```go\n", "```\n", strings.Repeat("a", 500)},
		{"tilde fence, ASCII line", "~~~text\n", "~~~\n", strings.Repeat("b", 500)},
		{"backtick fence, CJK line", "```go\n", "```\n", strings.Repeat("测", 500)},
		{"tilde fence, CJK line", "~~~text\n", "~~~\n", strings.Repeat("测", 500)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.open + tc.line + "\n" + tc.close
			chunks := SplitMarkdown(text, 50)
			if len(chunks) < 2 {
				t.Fatalf("expected the overlong line to force multiple chunks, got %d", len(chunks))
			}
			for i, c := range chunks {
				if n := utf8.RuneCountInString(c); n > 50 {
					t.Fatalf("chunk %d has %d runes, want <= 50: %q", i, n, c)
				}
			}
		})
	}
}

func TestSplitMarkdown_FenceOpeningAtChunkBoundaryStaysWithinBudget(t *testing.T) {
	cases := []string{
		strings.Repeat("a", 1993) + "\n```go\nx\n```\n",
		strings.Repeat("界", 1991) + "\n~~~json\nx\n~~~\n",
	}
	for _, text := range cases {
		for i, chunk := range SplitMarkdown(text, 2000) {
			if got := utf8.RuneCountInString(chunk); got > 2000 {
				t.Fatalf("chunk %d has %d runes, want <= 2000: %q", i, got, chunk[len(chunk)-min(len(chunk), 32):])
			}
		}
	}
}

func TestSplitMarkdown_PlainShortTextUnaffectedByFenceLogic(t *testing.T) {
	text := "just a normal short reply, no code fences here."
	chunks := SplitMarkdown(text, 2000)
	if len(chunks) != 1 || chunks[0] != text {
		t.Fatalf("unexpected chunking of plain text: %v", chunks)
	}
}
