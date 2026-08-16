package feishucard

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Renderer converts a goldmark AST into Feishu card markdown. Most syntax is
// passed through unchanged (Feishu Card JSON 2.0 handles it natively). Only
// GFM tables and task checkboxes are transformed.
type Renderer struct{}

// NewRenderer returns a renderer.NodeRenderer for Feishu card markdown.
func NewRenderer() renderer.NodeRenderer {
	return &Renderer{}
}

// RegisterFuncs registers render functions for all handled AST node kinds.
func (r *Renderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	// Structural
	reg.Register(ast.KindDocument, r.renderDocument)
	reg.Register(ast.KindParagraph, r.renderParagraph)
	reg.Register(ast.KindTextBlock, r.renderTextBlock)
	reg.Register(ast.KindThematicBreak, r.renderThematicBreak)

	// Inline
	reg.Register(ast.KindText, r.renderText)
	reg.Register(ast.KindString, r.renderString)
	reg.Register(ast.KindEmphasis, r.renderEmphasis)
	reg.Register(ast.KindCodeSpan, r.renderCodeSpan)
	reg.Register(ast.KindAutoLink, r.renderAutoLink)
	reg.Register(ast.KindLink, r.renderLink)
	reg.Register(ast.KindImage, r.renderImage)
	reg.Register(ast.KindRawHTML, r.renderRawHTML)
	reg.Register(ast.KindHTMLBlock, r.renderHTMLBlock)

	// Block
	reg.Register(ast.KindHeading, r.renderHeading)
	reg.Register(ast.KindBlockquote, r.renderBlockquote)
	reg.Register(ast.KindList, r.renderList)
	reg.Register(ast.KindListItem, r.renderListItem)
	reg.Register(ast.KindFencedCodeBlock, r.renderFencedCodeBlock)
	reg.Register(ast.KindCodeBlock, r.renderCodeBlock)

	// GFM extensions
	reg.Register(east.KindStrikethrough, r.renderStrikethrough)
	reg.Register(east.KindTable, r.renderTable)
	reg.Register(east.KindTableHeader, r.renderTableHeader)
	reg.Register(east.KindTableRow, r.renderTableRow)
	reg.Register(east.KindTableCell, r.renderTableCell)
	reg.Register(east.KindTaskCheckBox, r.renderTaskCheckBox)
}

// --- Structural ---

func (r *Renderer) renderDocument(_ util.BufWriter, _ []byte, _ ast.Node, _ bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *Renderer) renderParagraph(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		_ = w.WriteByte('\n')
		// Add blank line between sibling paragraphs, but not inside list items
		// where each item already handles its own line break.
		if node.NextSibling() != nil && node.Parent().Kind() != ast.KindListItem {
			_ = w.WriteByte('\n')
		}
	}
	return ast.WalkContinue, nil
}

// renderTextBlock handles tight list item content (no <p> wrapper in HTML).
func (r *Renderer) renderTextBlock(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		_ = w.WriteByte('\n')
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderThematicBreak(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("---\n")
		if node.NextSibling() != nil {
			_ = w.WriteByte('\n')
		}
	}
	return ast.WalkContinue, nil
}

// --- Inline ---

func (r *Renderer) renderText(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Text)
	_, _ = w.Write(n.Segment.Value(source))
	if n.SoftLineBreak() {
		_ = w.WriteByte('\n')
	}
	if n.HardLineBreak() {
		_, _ = w.WriteString("\n\n")
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderString(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	_, _ = w.Write(node.(*ast.String).Value)
	return ast.WalkContinue, nil
}

func (r *Renderer) renderEmphasis(w util.BufWriter, _ []byte, node ast.Node, _ bool) (ast.WalkStatus, error) {
	n := node.(*ast.Emphasis)
	if n.Level == 2 {
		_, _ = w.WriteString("**")
	} else {
		_ = w.WriteByte('*')
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderCodeSpan(w util.BufWriter, _ []byte, _ ast.Node, _ bool) (ast.WalkStatus, error) {
	_ = w.WriteByte('`')
	return ast.WalkContinue, nil
}

func (r *Renderer) renderAutoLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.AutoLink)
	// Label/URL slice into source; passing nil panics on any autolink.
	_, _ = w.WriteString("[")
	_, _ = w.Write(n.Label(source))
	_, _ = w.WriteString("](")
	_, _ = w.Write(n.URL(source))
	_ = w.WriteByte(')')
	return ast.WalkSkipChildren, nil
}

func (r *Renderer) renderLink(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	if entering {
		_ = w.WriteByte('[')
	} else {
		_, _ = w.WriteString("](")
		_, _ = w.Write(n.Destination)
		_ = w.WriteByte(')')
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderImage(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Image)
	if entering {
		_, _ = w.WriteString("![")
	} else {
		_, _ = w.WriteString("](")
		_, _ = w.Write(n.Destination)
		_ = w.WriteByte(')')
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.RawHTML)
	for i := 0; i < n.Segments.Len(); i++ {
		seg := n.Segments.At(i)
		_, _ = w.Write(seg.Value(source))
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.HTMLBlock)
	for i := 0; i < n.Lines().Len(); i++ {
		line := n.Lines().At(i)
		_, _ = w.Write(line.Value(source))
	}
	return ast.WalkContinue, nil
}

// --- Block ---

func (r *Renderer) renderHeading(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Heading)
	if entering {
		_, _ = w.WriteString(strings.Repeat("#", n.Level))
		_ = w.WriteByte(' ')
	} else {
		_ = w.WriteByte('\n')
		if node.NextSibling() != nil {
			_ = w.WriteByte('\n')
		}
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderBlockquote(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("> ")
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderList(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering && node.NextSibling() != nil {
		_ = w.WriteByte('\n')
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderListItem(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	list := node.Parent().(*ast.List)
	indent := listDepth(node) - 1
	prefix := strings.Repeat("    ", indent)
	if list.IsOrdered() {
		idx := list.Start
		for c := list.FirstChild(); c != nil && c != node; c = c.NextSibling() {
			idx++
		}
		_, _ = fmt.Fprintf(w, "%s%d. ", prefix, idx)
	} else {
		_, _ = fmt.Fprintf(w, "%s- ", prefix)
	}
	return ast.WalkContinue, nil
}

func (r *Renderer) renderFencedCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.FencedCodeBlock)
	_, _ = w.WriteString("```")
	if lang := n.Language(source); len(lang) > 0 {
		_, _ = w.Write(lang)
	}
	_ = w.WriteByte('\n')
	writeLines(w, source, n.Lines())
	_, _ = w.WriteString("```\n")
	if node.NextSibling() != nil {
		_ = w.WriteByte('\n')
	}
	return ast.WalkSkipChildren, nil
}

func (r *Renderer) renderCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.CodeBlock)
	_, _ = w.WriteString("```\n")
	writeLines(w, source, n.Lines())
	_, _ = w.WriteString("```\n")
	if node.NextSibling() != nil {
		_ = w.WriteByte('\n')
	}
	return ast.WalkSkipChildren, nil
}

// --- GFM extensions ---

func (r *Renderer) renderStrikethrough(w util.BufWriter, _ []byte, _ ast.Node, _ bool) (ast.WalkStatus, error) {
	_, _ = w.WriteString("~~")
	return ast.WalkContinue, nil
}

// renderTable converts a GFM table into a plain-text aligned table inside a
// code block. This bypasses Feishu's 5-row display limit for native tables.
func (r *Renderer) renderTable(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}

	table := node.(*east.Table)
	rows := collectTableRows(source, table)
	if len(rows) == 0 {
		return ast.WalkSkipChildren, nil
	}

	widths := columnWidths(rows)

	_, _ = w.WriteString("```\n")
	for i, row := range rows {
		for j, cell := range row {
			if j > 0 {
				_, _ = w.WriteString(" | ")
			}
			padded := padCell(cell, widths[j], alignmentAt(table, j))
			_, _ = w.WriteString(padded)
		}
		_ = w.WriteByte('\n')
		// Separator after header row.
		if i == 0 {
			for j := range widths {
				if j > 0 {
					_, _ = w.WriteString("-+-")
				}
				_, _ = w.WriteString(strings.Repeat("-", widths[j]))
			}
			_ = w.WriteByte('\n')
		}
	}
	_, _ = w.WriteString("```\n")
	if node.NextSibling() != nil {
		_ = w.WriteByte('\n')
	}

	return ast.WalkSkipChildren, nil
}

// Table sub-nodes are handled entirely in renderTable via AST traversal.
func (r *Renderer) renderTableHeader(_ util.BufWriter, _ []byte, _ ast.Node, _ bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *Renderer) renderTableRow(_ util.BufWriter, _ []byte, _ ast.Node, _ bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *Renderer) renderTableCell(_ util.BufWriter, _ []byte, _ ast.Node, _ bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *Renderer) renderTaskCheckBox(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	if node.(*east.TaskCheckBox).IsChecked {
		_, _ = w.WriteString("✅ ")
	} else {
		_, _ = w.WriteString("☐ ")
	}
	return ast.WalkContinue, nil
}

// --- Helpers ---

func writeLines(w util.BufWriter, source []byte, lines *text.Segments) {
	for i := 0; i < lines.Len(); i++ {
		line := lines.At(i)
		_, _ = w.Write(line.Value(source))
	}
}

func listDepth(node ast.Node) int {
	depth := 0
	for p := node.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == ast.KindList {
			depth++
		}
	}
	return depth
}

// collectTableRows extracts all cell text from a table into a 2D string slice.
func collectTableRows(source []byte, table *east.Table) [][]string {
	var rows [][]string
	for child := table.FirstChild(); child != nil; child = child.NextSibling() {
		var row []string
		for cell := child.FirstChild(); cell != nil; cell = cell.NextSibling() {
			row = append(row, cellText(source, cell))
		}
		rows = append(rows, row)
	}
	return rows
}

func cellText(source []byte, node ast.Node) string {
	var buf bytes.Buffer
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		inlineText(source, c, &buf)
	}
	return strings.TrimSpace(buf.String())
}

func inlineText(source []byte, node ast.Node, buf *bytes.Buffer) {
	switch n := node.(type) {
	case *ast.Text:
		buf.Write(n.Segment.Value(source))
	case *ast.String:
		buf.Write(n.Value)
	case *ast.CodeSpan:
		buf.WriteByte('`')
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			inlineText(source, c, buf)
		}
		buf.WriteByte('`')
		return
	case *ast.Emphasis:
		marker := "*"
		if n.Level == 2 {
			marker = "**"
		}
		buf.WriteString(marker)
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			inlineText(source, c, buf)
		}
		buf.WriteString(marker)
		return
	case *ast.Link:
		buf.WriteByte('[')
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			inlineText(source, c, buf)
		}
		buf.WriteString("](")
		buf.Write(n.Destination)
		buf.WriteByte(')')
		return
	}
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		inlineText(source, c, buf)
	}
}

func columnWidths(rows [][]string) []int {
	cols := 0
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	widths := make([]int, cols)
	for _, row := range rows {
		for j, cell := range row {
			if n := len([]rune(cell)); n > widths[j] {
				widths[j] = n
			}
		}
	}
	return widths
}

func padCell(s string, width int, align east.Alignment) string {
	pad := width - len([]rune(s))
	if pad <= 0 {
		return s
	}
	switch align {
	case east.AlignRight:
		return strings.Repeat(" ", pad) + s
	case east.AlignCenter:
		left := pad / 2
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", pad-left)
	default:
		return s + strings.Repeat(" ", pad)
	}
}

func alignmentAt(table *east.Table, col int) east.Alignment {
	if col < len(table.Alignments) {
		return table.Alignments[col]
	}
	return east.AlignNone
}
