package agent

import (
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

// NormalizeToolResult removes render-reference sentinels from every text block
// and carries their deduplicated references on the result sideband. It is safe
// to call again at event boundaries, which keeps native and code paths aligned.
func NormalizeToolResult(result ai.ToolResultMessage) ai.ToolResultMessage {
	blocks := make([]ai.ContentBlock, len(result.Content))
	copy(blocks, result.Content)
	refs := append([]renderrefs.Reference(nil), result.References...)
	for i, block := range blocks {
		text, ok := block.(ai.TextContent)
		if !ok {
			continue
		}
		clean, extracted := renderrefs.Extract(text.Text)
		text.Text = clean
		blocks[i] = text
		refs = append(refs, extracted...)
	}
	result.Content = blocks
	result.References = dedupeReferences(refs)
	return result
}

func dedupeReferences(refs []renderrefs.Reference) []renderrefs.Reference {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(refs))
	out := make([]renderrefs.Reference, 0, len(refs))
	for _, ref := range refs {
		// Type and ID are the durable render-reference identity. Preserve the
		// first occurrence so a later, conflicting preview cannot rewrite it.
		key := ref.Type + "\x00" + ref.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	return out
}
