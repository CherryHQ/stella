package agent

import (
	"encoding/json"

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
		blocks[i] = ai.TextContent{Text: clean}
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
		key, err := json.Marshal(ref)
		if err != nil {
			continue
		}
		if _, ok := seen[string(key)]; ok {
			continue
		}
		seen[string(key)] = struct{}{}
		out = append(out, ref)
	}
	return out
}
