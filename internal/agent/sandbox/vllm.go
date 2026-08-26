package sandbox

import pkgtools "github.com/CherryHQ/stella/pkg/tools"

// vllmDefinition keeps the historical name reserved so plugins and overrides
// cannot claim a name that older sessions may still reference. It is not part
// of the model-facing catalog or runtime tool set.
func vllmDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "vllm",
		Description: "Reserved legacy image-inspection tool name.",
		InputSchema: map[string]any{"type": "object"},
	}
}
