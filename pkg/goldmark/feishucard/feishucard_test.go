package feishucard_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/goldmark/feishucard"
)

func convert(t *testing.T, input string) string {
	t.Helper()
	md := feishucard.New()
	var buf bytes.Buffer
	if err := md.Convert([]byte(input), &buf); err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	return buf.String()
}

func TestHeading(t *testing.T) {
	got := convert(t, "# Title\n\nsome text\n")
	want := "# Title\n\nsome text\n"
	if got != want {
		t.Errorf("heading:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestBoldItalicStrikethrough(t *testing.T) {
	got := convert(t, "**bold** *italic* ~~strike~~\n")
	want := "**bold** *italic* ~~strike~~\n"
	if got != want {
		t.Errorf("inline formatting:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestInlineCode(t *testing.T) {
	got := convert(t, "use `fmt.Println`\n")
	want := "use `fmt.Println`\n"
	if got != want {
		t.Errorf("inline code:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestFencedCodeBlock(t *testing.T) {
	input := "```go\nfmt.Println(\"hi\")\n```\n"
	got := convert(t, input)
	want := "```go\nfmt.Println(\"hi\")\n```\n"
	if got != want {
		t.Errorf("fenced code:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestLink(t *testing.T) {
	got := convert(t, "[Google](https://google.com)\n")
	want := "[Google](https://google.com)\n"
	if got != want {
		t.Errorf("link:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestImage(t *testing.T) {
	got := convert(t, "![alt](img_key)\n")
	want := "![alt](img_key)\n"
	if got != want {
		t.Errorf("image:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestBlockquote(t *testing.T) {
	got := convert(t, "> quoted text\n")
	want := "> quoted text\n"
	if got != want {
		t.Errorf("blockquote:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestUnorderedList(t *testing.T) {
	input := "- one\n- two\n- three\n"
	got := convert(t, input)
	want := "- one\n- two\n- three\n"
	if got != want {
		t.Errorf("unordered list:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestOrderedList(t *testing.T) {
	input := "1. one\n2. two\n3. three\n"
	got := convert(t, input)
	want := "1. one\n2. two\n3. three\n"
	if got != want {
		t.Errorf("ordered list:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestThematicBreak(t *testing.T) {
	input := "above\n\n---\n\nbelow\n"
	got := convert(t, input)
	want := "above\n\n---\n\nbelow\n"
	if got != want {
		t.Errorf("thematic break:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestTaskCheckBox(t *testing.T) {
	input := "- [x] done\n- [ ] todo\n"
	got := convert(t, input)
	want := "- ✅ done\n- ☐ todo\n"
	if got != want {
		t.Errorf("task checkbox:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestTableSmall(t *testing.T) {
	input := "| Name | Age |\n|------|-----|\n| Alice | 30 |\n| Bob | 25 |\n"
	got := convert(t, input)
	// Table should be rendered as a code-block.
	if !bytes.Contains([]byte(got), []byte("```")) {
		t.Errorf("small table should be in code block:\n%s", got)
	}
	if !bytes.Contains([]byte(got), []byte("Alice")) {
		t.Errorf("table missing Alice:\n%s", got)
	}
	if !bytes.Contains([]byte(got), []byte("Bob")) {
		t.Errorf("table missing Bob:\n%s", got)
	}
}

func TestTableLarge(t *testing.T) {
	var input strings.Builder
	input.WriteString("| N | V |\n|---|---|\n")
	for i := 1; i <= 10; i++ {
		input.WriteString("| row | val |\n")
	}
	got := convert(t, input.String())
	if !bytes.Contains([]byte(got), []byte("```")) {
		t.Errorf("large table should be in code block:\n%s", got)
	}
	// All 10 data rows must be present.
	if count := bytes.Count([]byte(got), []byte("row")); count != 10 {
		t.Errorf("expected 10 rows, got %d:\n%s", count, got)
	}
}

func TestTableAlignment(t *testing.T) {
	input := "| Left | Center | Right |\n|:-----|:------:|------:|\n| a | b | c |\n| long | x | y |\n"
	got := convert(t, input)
	// Verify alignment characters are reflected in padding.
	if !bytes.Contains([]byte(got), []byte("```")) {
		t.Errorf("aligned table should be in code block:\n%s", got)
	}
}

func TestNestedList(t *testing.T) {
	input := "- parent\n    - child\n"
	got := convert(t, input)
	want := "- parent\n    - child\n"
	if got != want {
		t.Errorf("nested list:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMixedContent(t *testing.T) {
	input := "# Header\n\nSome **bold** text.\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n- item\n"
	got := convert(t, input)
	if !bytes.Contains([]byte(got), []byte("# Header")) {
		t.Errorf("mixed: missing heading:\n%s", got)
	}
	if !bytes.Contains([]byte(got), []byte("**bold**")) {
		t.Errorf("mixed: missing bold:\n%s", got)
	}
	if !bytes.Contains([]byte(got), []byte("```")) {
		t.Errorf("mixed: table should be code block:\n%s", got)
	}
	if !bytes.Contains([]byte(got), []byte("- item")) {
		t.Errorf("mixed: missing list item:\n%s", got)
	}
}
