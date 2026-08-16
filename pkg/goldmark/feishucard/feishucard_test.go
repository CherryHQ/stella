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

func TestAutoLink(t *testing.T) {
	got := convert(t, "see <https://google.com> now\n")
	want := "see [https://google.com](https://google.com) now\n"
	if got != want {
		t.Errorf("autolink:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestRenderAutoLink(t *testing.T) {
	elements := feishucard.Render("visit <https://example.com> and <mail@example.com>\n")
	if len(elements) != 1 {
		t.Fatalf("elements: got %d, want 1", len(elements))
	}
	content, _ := elements[0]["content"].(string)
	if !strings.Contains(content, "(https://example.com)") {
		t.Errorf("autolink url missing: %q", content)
	}
	if !strings.Contains(content, "[mail@example.com](") {
		t.Errorf("email autolink missing: %q", content)
	}
}

func TestRenderDetailsBecomesPanel(t *testing.T) {
	input := "before\n\n<details>\n<summary>点击展开</summary>\n\n内容一\n\n```go\nx := 1\n```\n\n</details>\n\nafter\n"
	elements := feishucard.Render(input)
	if len(elements) != 3 {
		t.Fatalf("elements: got %d, want 3 (%v)", len(elements), elements)
	}
	if elements[0]["content"] != "before" || elements[2]["content"] != "after" {
		t.Errorf("surrounding markdown: got %v / %v", elements[0], elements[2])
	}

	panel := elements[1]
	if panel["tag"] != "collapsible_panel" {
		t.Fatalf("tag: got %v, want collapsible_panel", panel["tag"])
	}
	if panel["expanded"] != false {
		t.Errorf("expanded: got %v, want false", panel["expanded"])
	}
	header, _ := panel["header"].(map[string]any)
	title, _ := header["title"].(map[string]any)
	if title["content"] != "点击展开" {
		t.Errorf("header title: got %v, want 点击展开", title["content"])
	}
	inner, _ := panel["elements"].([]map[string]any)
	if len(inner) != 1 {
		t.Fatalf("panel elements: got %d, want 1 (%v)", len(inner), inner)
	}
	content, _ := inner[0]["content"].(string)
	if !strings.Contains(content, "内容一") || !strings.Contains(content, "```go") {
		t.Errorf("panel content: got %q", content)
	}
	if strings.Contains(content, "<details") || strings.Contains(content, "<summary") {
		t.Errorf("html tags leaked into panel content: %q", content)
	}
}

func TestRenderDetailsOpenAndNoSummary(t *testing.T) {
	elements := feishucard.Render("<details open>\njust text\n</details>\n")
	if len(elements) != 1 {
		t.Fatalf("elements: got %d, want 1", len(elements))
	}
	panel := elements[0]
	if panel["expanded"] != true {
		t.Errorf("expanded: got %v, want true", panel["expanded"])
	}
	header, _ := panel["header"].(map[string]any)
	title, _ := header["title"].(map[string]any)
	if title["content"] != "详情" {
		t.Errorf("default title: got %v", title["content"])
	}
}

func TestRenderDetailsWithTableInside(t *testing.T) {
	input := "<details>\n<summary>t</summary>\n\n| A | B |\n| - | - |\n| 1 | 2 |\n\n</details>\n"
	elements := feishucard.Render(input)
	inner, _ := elements[0]["elements"].([]map[string]any)
	if len(inner) != 1 || inner[0]["tag"] != "table" {
		t.Fatalf("panel elements: got %v, want one native table", inner)
	}
}

func TestRenderDetailsInCodeFenceStaysMarkdown(t *testing.T) {
	input := "```html\n<details>\n<summary>x</summary>\n</details>\n```\n"
	elements := feishucard.Render(input)
	if len(elements) != 1 || elements[0]["tag"] != "markdown" {
		t.Fatalf("elements: got %v, want single markdown", elements)
	}
	if !strings.Contains(elements[0]["content"].(string), "<details>") {
		t.Errorf("code fence content lost: %v", elements[0]["content"])
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

// --- Button tests ---

func TestButtonSingle(t *testing.T) {
	input := "Some text\n\n{{button value=\"retry\" type=\"primary\" label=\"Retry\"}}\n"
	elems := feishucard.Render(input)
	if len(elems) != 2 {
		t.Fatalf("expected 2 elements (md, button), got %d: %v", len(elems), elemTags(elems))
	}
	if elems[0]["tag"] != "markdown" {
		t.Errorf("elem 0: want markdown, got %v", elems[0]["tag"])
	}
	if elems[1]["tag"] != "button" {
		t.Errorf("elem 1: want button, got %v", elems[1]["tag"])
	}
	text := elems[1]["text"].(map[string]any)
	if text["content"] != "Retry" {
		t.Errorf("button label = %q, want Retry", text["content"])
	}
	if elems[1]["type"] != "primary" {
		t.Errorf("button type = %v, want primary", elems[1]["type"])
	}
}

func TestButtonOpenURL(t *testing.T) {
	input := "{{button label=\"Open\" url=\"https://stella.example.com/agents/a1/tasks/t1\"}}\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 || elems[0]["tag"] != "button" {
		t.Fatalf("expected 1 button element, got %v", elemTags(elems))
	}
	behaviors := elems[0]["behaviors"].([]map[string]any)
	if len(behaviors) != 1 || behaviors[0]["type"] != "open_url" {
		t.Fatalf("want open_url behavior, got %v", behaviors)
	}
	if behaviors[0]["default_url"] != "https://stella.example.com/agents/a1/tasks/t1" {
		t.Errorf("default_url = %v", behaviors[0]["default_url"])
	}
}

func TestButtonGrouped(t *testing.T) {
	input := "{{button value=\"yes\" label=\"Yes\"}}\n{{button value=\"no\" label=\"No\"}}\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 {
		t.Fatalf("expected 1 element (column_set), got %d: %v", len(elems), elemTags(elems))
	}
	if elems[0]["tag"] != "column_set" {
		t.Fatalf("expected column_set, got %v", elems[0]["tag"])
	}
	cols := elems[0]["columns"].([]map[string]any)
	if len(cols) != 2 {
		t.Errorf("expected 2 columns, got %d", len(cols))
	}
}

func TestButtonWithConfirm(t *testing.T) {
	input := "{{button value=\"delete\" type=\"danger\" confirm=\"Are you sure?\" label=\"Delete\"}}\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 || elems[0]["tag"] != "button" {
		t.Fatalf("expected 1 button element, got %v", elemTags(elems))
	}
	confirm, ok := elems[0]["confirm"].(map[string]any)
	if !ok {
		t.Fatalf("confirm missing")
	}
	confirmText := confirm["text"].(map[string]any)
	if confirmText["content"] != "Are you sure?" {
		t.Errorf("confirm text = %q, want 'Are you sure?'", confirmText["content"])
	}
}

func TestButtonInlineWithText(t *testing.T) {
	input := "Click {{button value=\"go\" label=\"Go\"}} to continue.\n"
	elems := feishucard.Render(input)
	// Should produce: markdown("Click"), button, markdown("to continue.")
	if len(elems) != 3 {
		t.Fatalf("expected 3 elements, got %d: %v", len(elems), elemTags(elems))
	}
	if elems[0]["tag"] != "markdown" || elems[1]["tag"] != "button" || elems[2]["tag"] != "markdown" {
		t.Errorf("unexpected tags: %v", elemTags(elems))
	}
}

func TestButtonNoButtons(t *testing.T) {
	input := "Just plain text, no buttons.\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 || elems[0]["tag"] != "markdown" {
		t.Errorf("expected single markdown element, got %v", elemTags(elems))
	}
}

func TestButtonMissingAttrs(t *testing.T) {
	// Missing label — should be treated as plain text.
	input := "{{button value=\"retry\"}}\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 || elems[0]["tag"] != "markdown" {
		t.Errorf("malformed button should stay as markdown, got %v", elemTags(elems))
	}
}

func TestButtonDefaultType(t *testing.T) {
	input := "{{button value=\"ok\" label=\"OK\"}}\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 || elems[0]["tag"] != "button" {
		t.Fatalf("expected 1 button, got %v", elemTags(elems))
	}
	if elems[0]["type"] != "default" {
		t.Errorf("default type = %v, want 'default'", elems[0]["type"])
	}
}

func TestButtonInsideCodeStaysMarkdown(t *testing.T) {
	input := "```\n{{button value=\"retry\" label=\"Retry\"}}\n```\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 || elems[0]["tag"] != "markdown" {
		t.Fatalf("button in code should stay markdown, got %v", elemTags(elems))
	}
	content := elems[0]["content"].(string)
	if !strings.Contains(content, "{{button") || !strings.Contains(content, "```") {
		t.Errorf("code button not preserved: %q", content)
	}
}

func TestButtonInsideInlineCodeStaysMarkdown(t *testing.T) {
	input := "Use `{{button value=\"retry\" label=\"Retry\"}}` literally.\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 || elems[0]["tag"] != "markdown" {
		t.Fatalf("inline-code button should stay markdown, got %v", elemTags(elems))
	}
	if content := elems[0]["content"].(string); !strings.Contains(content, "{{button") {
		t.Errorf("inline-code button not preserved: %q", content)
	}
}

func TestButtonMalformedInlinePreserved(t *testing.T) {
	input := "Before {{button value=\"retry\"}} after\n"
	elems := feishucard.Render(input)
	if len(elems) != 1 || elems[0]["tag"] != "markdown" {
		t.Fatalf("malformed inline button should stay markdown, got %v", elemTags(elems))
	}
	content := elems[0]["content"].(string)
	if !strings.Contains(content, "{{button value=\"retry\"}}") {
		t.Errorf("malformed button not preserved: %q", content)
	}
}

func TestButtonMixedWithTable(t *testing.T) {
	input := "| A | B |\n|---|---|\n| 1 | 2 |\n\n{{button value=\"export\" type=\"primary\" label=\"Export\"}}\n"
	elems := feishucard.Render(input)
	tags := elemTags(elems)
	if len(elems) != 2 {
		t.Fatalf("expected 2 elements (table, button), got %d: %v", len(elems), tags)
	}
	if tags[0] != "table" || tags[1] != "button" {
		t.Errorf("unexpected tags: %v", tags)
	}
}

// TestRenderFullDocument is an end-to-end golden test that feeds a complete
// markdown document through Render() and verifies the full element structure.
func TestRenderFullDocument(t *testing.T) {
	input := "# Summary Report\n\n" +
		"Here is **bold**, *italic*, ~~strikethrough~~, and `inline code`.\n\n" +
		"| Name  | Score |\n|-------|-------|\n| Alice | 95    |\n| Bob   | 87    |\n\n" +
		"> Important note here.\n\n" +
		"- Item one\n- Item two\n    - Nested item\n\n" +
		"1. First\n2. Second\n\n" +
		"- [x] Completed\n- [ ] Pending\n\n" +
		"[Visit](https://example.com) and ![logo](img_key)\n\n" +
		"```python\nprint(\"hello\")\n```\n\n" +
		"---\n\n" +
		"Results are ready.\n\n" +
		"{{button value=\"export\" type=\"primary\" label=\"Export\"}}\n\n" +
		"Choose an option:\n\n" +
		"{{button value=\"yes\" label=\"Yes\"}}\n" +
		"{{button value=\"no\" label=\"No\"}}\n"

	elems := feishucard.Render(input)

	// 6 elements: md (heading+para), table, md (blockquote..text), button, md (separator), column_set
	wantTags := []string{"markdown", "table", "markdown", "button", "markdown", "column_set"}
	gotTags := elemTags(elems)
	if len(elems) != len(wantTags) {
		t.Fatalf("element count: got %d %v, want %d %v", len(elems), gotTags, len(wantTags), wantTags)
	}
	for i, want := range wantTags {
		if gotTags[i] != want {
			t.Errorf("elem[%d] tag: got %q, want %q", i, gotTags[i], want)
		}
	}

	// --- elem[0]: markdown with heading + inline formatting ---
	md0 := elems[0]["content"].(string)
	for _, sub := range []string{
		"# Summary Report",
		"**bold**",
		"*italic*",
		"~~strikethrough~~",
		"`inline code`",
	} {
		if !strings.Contains(md0, sub) {
			t.Errorf("elem[0] missing %q in:\n%s", sub, md0)
		}
	}

	// --- elem[1]: native table ---
	cols := elems[1]["columns"].([]map[string]any)
	if len(cols) != 2 {
		t.Fatalf("table: got %d columns, want 2", len(cols))
	}
	if cols[0]["display_name"] != "Name" {
		t.Errorf("table col[0] display_name: got %q, want Name", cols[0]["display_name"])
	}
	if cols[1]["display_name"] != "Score" {
		t.Errorf("table col[1] display_name: got %q, want Score", cols[1]["display_name"])
	}
	rows := elems[1]["rows"].([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("table: got %d rows, want 2", len(rows))
	}
	if rows[0]["c0"] != "Alice" || rows[0]["c1"] != "95" {
		t.Errorf("table row[0]: got %v, want Alice/95", rows[0])
	}
	if rows[1]["c0"] != "Bob" || rows[1]["c1"] != "87" {
		t.Errorf("table row[1]: got %v, want Bob/87", rows[1])
	}
	if elems[1]["page_size"] != 2 {
		t.Errorf("table page_size: got %v, want 2", elems[1]["page_size"])
	}
	headerStyle := elems[1]["header_style"].(map[string]any)
	if headerStyle["bold"] != true {
		t.Errorf("table header_style.bold: got %v", headerStyle["bold"])
	}

	// --- elem[2]: markdown with blockquote, lists, code block, hr, text ---
	md2 := elems[2]["content"].(string)
	for _, sub := range []string{
		"> Important note here.",
		"- Item one",
		"    - Nested item",
		"1. First",
		"2. Second",
		"✅ Completed",
		"☐ Pending",
		"[Visit](https://example.com)",
		"![logo](img_key)",
		"```python",
		"print(\"hello\")",
		"---",
		"Results are ready.",
	} {
		if !strings.Contains(md2, sub) {
			t.Errorf("elem[2] missing %q in:\n%s", sub, md2)
		}
	}

	// --- elem[3]: standalone export button ---
	if elems[3]["type"] != "primary" {
		t.Errorf("export button type: got %v, want primary", elems[3]["type"])
	}
	btnText := elems[3]["text"].(map[string]any)
	if btnText["content"] != "Export" {
		t.Errorf("export button label: got %q, want Export", btnText["content"])
	}
	behaviors := elems[3]["behaviors"].([]map[string]any)
	if val := behaviors[0]["value"].(map[string]any)["action"]; val != "export" {
		t.Errorf("export button action: got %v, want export", val)
	}

	// --- elem[4]: markdown separator between button groups ---
	md4 := elems[4]["content"].(string)
	if !strings.Contains(md4, "Choose an option") {
		t.Errorf("elem[4] expected separator text, got: %q", md4)
	}

	// --- elem[5]: column_set with yes/no buttons ---
	csCols := elems[5]["columns"].([]map[string]any)
	if len(csCols) != 2 {
		t.Fatalf("column_set: got %d columns, want 2", len(csCols))
	}
	wantButtons := []struct{ label, action string }{
		{"Yes", "yes"},
		{"No", "no"},
	}
	for i, want := range wantButtons {
		colElems := csCols[i]["elements"].([]map[string]any)
		if len(colElems) != 1 || colElems[0]["tag"] != "button" {
			t.Errorf("column[%d]: expected 1 button, got %v", i, colElems)
			continue
		}
		btn := colElems[0]
		if txt := btn["text"].(map[string]any); txt["content"] != want.label {
			t.Errorf("column[%d] button label: got %q, want %q", i, txt["content"], want.label)
		}
		bvs := btn["behaviors"].([]map[string]any)
		if val := bvs[0]["value"].(map[string]any)["action"]; val != want.action {
			t.Errorf("column[%d] button action: got %v, want %q", i, val, want.action)
		}
		if btn["type"] != "default" {
			t.Errorf("column[%d] button type: got %v, want default", i, btn["type"])
		}
	}
}

func elemTags(elems []map[string]any) []string {
	tags := make([]string, len(elems))
	for i, e := range elems {
		tags[i] = fmt.Sprint(e["tag"])
	}
	return tags
}
