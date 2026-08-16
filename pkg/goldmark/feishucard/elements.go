package feishucard

import (
	"bytes"
	"fmt"
	"strings"

	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// maxNativeTables is the Feishu card limit for table components per card.
const maxNativeTables = 5

var md = New()

// Render parses standard markdown and returns a slice of Feishu card body
// elements ready for JSON marshaling into body.elements.
//
// GFM tables (up to 5 per card) become native table components with full
// pagination support. Remaining tables fall back to code-block rendering.
// <details> sections become native collapsible panels. All other content
// becomes markdown elements.
func Render(source string) []map[string]any {
	st := &renderState{}
	elements := st.render(source)
	if len(elements) == 0 {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": source,
		})
	}
	return elements
}

// renderState carries limits that apply per card, not per nested section.
type renderState struct {
	tableCount int
}

// render converts one markdown fragment, splitting out <details> sections
// before falling back to block rendering.
func (st *renderState) render(source string) []map[string]any {
	if elements, ok := st.renderDetails(source); ok {
		return elements
	}
	return st.renderBlocks(source)
}

func (st *renderState) renderBlocks(source string) []map[string]any {
	src := []byte(source)
	doc := md.Parser().Parse(text.NewReader(src))
	r := md.Renderer()

	var elements []map[string]any
	var mdBuf bytes.Buffer

	flush := func() {
		content := strings.TrimRight(mdBuf.String(), "\n")
		if content == "" {
			mdBuf.Reset()
			return
		}
		if extracted := extractButtons(content); extracted != nil {
			elements = append(elements, extracted...)
		} else {
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": content,
			})
		}
		mdBuf.Reset()
	}

	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		table, isTable := child.(*east.Table)
		if isTable && st.tableCount < maxNativeTables {
			st.tableCount++
			flush()
			elements = append(elements, buildTableElement(src, table))
		} else {
			// Non-table content, or table overflow — render to markdown.
			_ = r.Render(&mdBuf, src, child)
		}
	}
	flush()

	return elements
}

// buildTableElement converts a GFM table AST node into a Feishu native table
// component (tag: "table").
func buildTableElement(source []byte, table *east.Table) map[string]any {
	allRows := collectTableRows(source, table)
	if len(allRows) == 0 {
		return map[string]any{"tag": "markdown", "content": ""}
	}

	headers := allRows[0]
	dataRows := allRows[1:]

	columns := make([]map[string]any, len(headers))
	for i, h := range headers {
		col := map[string]any{
			"name":         fmt.Sprintf("c%d", i),
			"display_name": h,
			"data_type":    "lark_md",
		}
		if i < len(table.Alignments) {
			switch table.Alignments[i] {
			case east.AlignLeft:
				col["horizontal_align"] = "left"
			case east.AlignCenter:
				col["horizontal_align"] = "center"
			case east.AlignRight:
				col["horizontal_align"] = "right"
			}
		}
		columns[i] = col
	}

	rows := make([]map[string]any, len(dataRows))
	for i, row := range dataRows {
		r := make(map[string]any, len(headers))
		for j, cell := range row {
			r[fmt.Sprintf("c%d", j)] = cell
		}
		rows[i] = r
	}

	pageSize := max(min(len(rows), 10), 1)

	return map[string]any{
		"tag":        "table",
		"page_size":  pageSize,
		"row_height": "auto",
		"header_style": map[string]any{
			"bold":             true,
			"background_style": "grey",
		},
		"columns": columns,
		"rows":    rows,
	}
}
