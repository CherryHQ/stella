package mcp

import "github.com/CherryHQ/stella/internal/plugin"

// mustPublishedMCPTestSpec keeps package-level fixtures on the same canonical
// boundary as definitions loaded from the catalog.
func mustPublishedMCPTestSpec(raw string) []byte {
	spec, err := plugin.PublishDefinitionSpec([]byte(raw))
	if err != nil {
		panic(err)
	}
	return spec
}
