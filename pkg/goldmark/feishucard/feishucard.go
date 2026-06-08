package feishucard

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// New returns a goldmark.Markdown that renders standard markdown into the
// subset supported by Feishu Interactive Card JSON 2.0 markdown elements.
//
// Card JSON 2.0 natively supports most CommonMark syntax (headings, bold,
// italic, strikethrough, code, links, images, lists, blockquotes, thematic
// breaks). This renderer passes those through unchanged.
//
// Transformations applied:
//   - GFM tables → plain-text aligned table in a code block (bypasses the
//     5-row display limit of native Feishu table rendering)
//   - Task checkboxes → unicode symbols (✅ / ☐)
func New() goldmark.Markdown {
	md := goldmark.New(
		goldmark.WithRenderer(
			renderer.NewRenderer(
				renderer.WithNodeRenderers(util.Prioritized(NewRenderer(), 1000)),
			),
		),
	)

	// Register only the GFM parsers — we provide our own renderer, so we must
	// NOT use the full extensions (extension.Strikethrough, extension.Table,
	// extension.TaskList) which also register HTML renderers.
	md.Parser().AddOptions(
		parser.WithInlineParsers(
			util.Prioritized(extension.NewStrikethroughParser(), 500),
			util.Prioritized(extension.NewTaskCheckBoxParser(), 0),
		),
		parser.WithParagraphTransformers(
			util.Prioritized(extension.NewTableParagraphTransformer(), 200),
		),
		parser.WithASTTransformers(
			util.Prioritized(extension.NewTableASTTransformer(), 0),
		),
	)
	return md
}
