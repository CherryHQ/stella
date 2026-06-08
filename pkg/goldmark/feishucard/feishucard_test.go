package feishucard_test

import (
	"bytes"
	"fmt"
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

// --- String renderer tests (New / Convert) ---

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
	got := convert(t, "- one\n- two\n- three\n")
	want := "- one\n- two\n- three\n"
	if got != want {
		t.Errorf("unordered list:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestOrderedList(t *testing.T) {
	got := convert(t, "1. one\n2. two\n3. three\n")
	want := "1. one\n2. two\n3. three\n"
	if got != want {
		t.Errorf("ordered list:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestThematicBreak(t *testing.T) {
	got := convert(t, "above\n\n---\n\nbelow\n")
	want := "above\n\n---\n\nbelow\n"
	if got != want {
		t.Errorf("thematic break:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestTaskCheckBox(t *testing.T) {
	got := convert(t, "- [x] done\n- [ ] todo\n")
	want := "- ✅ done\n- ☐ todo\n"
	if got != want {
		t.Errorf("task checkbox:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestNestedList(t *testing.T) {
	got := convert(t, "- parent\n    - child\n")
	want := "- parent\n    - child\n"
	if got != want {
		t.Errorf("nested list:\ngot:  %q\nwant: %q", got, want)
	}
}

// --- Render() element tests ---

func TestRenderTextOnly(t *testing.T) {
	elems := feishucard.Render("# Hello\n\nworld\n")
	if len(elems) != 1 {
		t.Fatalf("expected 1 element, got %d", len(elems))
	}
	if elems[0]["tag"] != "markdown" {
		t.Errorf("expected markdown element, got %v", elems[0]["tag"])
	}
	content := elems[0]["content"].(string)
	if !strings.Contains(content, "# Hello") {
		t.Errorf("missing heading in content: %q", content)
	}
}

func TestRenderTableBecomesNative(t *testing.T) {
	input := "| Name | Age |\n|------|-----|\n| Alice | 30 |\n| Bob | 25 |\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 {
		t.Fatalf("expected 1 element, got %d", len(elems))
	}
	if elems[0]["tag"] != "table" {
		t.Fatalf("expected table element, got %v", elems[0]["tag"])
	}
	cols, ok := elems[0]["columns"].([]map[string]any)
	if !ok {
		t.Fatalf("columns not []map[string]any: %T", elems[0]["columns"])
	}
	if len(cols) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(cols))
	}
	if cols[0]["display_name"] != "Name" {
		t.Errorf("column 0 display_name = %q, want Name", cols[0]["display_name"])
	}
	rows, ok := elems[0]["rows"].([]map[string]any)
	if !ok {
		t.Fatalf("rows not []map[string]any: %T", elems[0]["rows"])
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 data rows, got %d", len(rows))
	}
	if rows[0]["c0"] != "Alice" {
		t.Errorf("row 0 c0 = %q, want Alice", rows[0]["c0"])
	}
}

func TestRenderMixedContent(t *testing.T) {
	input := "# Header\n\nSome text.\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\nMore text.\n"
	elems := feishucard.Render(input)
	if len(elems) != 3 {
		t.Fatalf("expected 3 elements (md, table, md), got %d: %v", len(elems), elemTags(elems))
	}
	if elems[0]["tag"] != "markdown" {
		t.Errorf("elem 0: want markdown, got %v", elems[0]["tag"])
	}
	if elems[1]["tag"] != "table" {
		t.Errorf("elem 1: want table, got %v", elems[1]["tag"])
	}
	if elems[2]["tag"] != "markdown" {
		t.Errorf("elem 2: want markdown, got %v", elems[2]["tag"])
	}
}

func TestRenderTableLargePageSize(t *testing.T) {
	var input strings.Builder
	input.WriteString("| K | V |\n|---|---|\n")
	for i := range 15 {
		fmt.Fprintf(&input, "| r%d | v%d |\n", i, i)
	}
	elems := feishucard.Render(input.String())
	if len(elems) != 1 || elems[0]["tag"] != "table" {
		t.Fatalf("expected 1 table element, got %v", elemTags(elems))
	}
	rows := elems[0]["rows"].([]map[string]any)
	if len(rows) != 15 {
		t.Errorf("expected 15 rows, got %d", len(rows))
	}
	if elems[0]["page_size"] != 10 {
		t.Errorf("page_size = %v, want 10", elems[0]["page_size"])
	}
}

func TestRenderTableAlignment(t *testing.T) {
	input := "| L | C | R |\n|:--|:-:|--:|\n| a | b | c |\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 || elems[0]["tag"] != "table" {
		t.Fatalf("expected 1 table element")
	}
	cols := elems[0]["columns"].([]map[string]any)
	if cols[0]["horizontal_align"] != "left" {
		t.Errorf("col 0 align = %v, want left", cols[0]["horizontal_align"])
	}
	if cols[1]["horizontal_align"] != "center" {
		t.Errorf("col 1 align = %v, want center", cols[1]["horizontal_align"])
	}
	if cols[2]["horizontal_align"] != "right" {
		t.Errorf("col 2 align = %v, want right", cols[2]["horizontal_align"])
	}
}

func TestRenderTableOverflowFallback(t *testing.T) {
	var input strings.Builder
	for i := range 6 {
		fmt.Fprintf(&input, "| H%d |\n|---|\n| d%d |\n\n", i, i)
	}
	elems := feishucard.Render(input.String())
	tableCount := 0
	codeBlockCount := 0
	for _, e := range elems {
		switch e["tag"] {
		case "table":
			tableCount++
		case "markdown":
			if strings.Contains(e["content"].(string), "```") {
				codeBlockCount++
			}
		}
	}
	if tableCount != 5 {
		t.Errorf("expected 5 native tables, got %d", tableCount)
	}
	if codeBlockCount < 1 {
		t.Errorf("expected 6th table to fall back to code block, got %d code blocks", codeBlockCount)
	}
}

func elemTags(elems []map[string]any) []string {
	tags := make([]string, len(elems))
	for i, e := range elems {
		tags[i] = fmt.Sprint(e["tag"])
	}
	return tags
}
