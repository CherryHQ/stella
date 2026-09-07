package main

import (
	"context"

	"github.com/CherryHQ/stella/internal/platform/toolinstall"
	"github.com/CherryHQ/stella/internal/plugin"
)

// warmAgentPackageArtifacts translates release defaults into cache inputs.
// Selection identity and command exposure belong to admitted sessions.
func warmAgentPackageArtifacts(ctx context.Context, home string, definitions []plugin.Definition) error {
	var tools []toolinstall.Tool
	for _, definition := range definitions {
		if !definition.DefaultEnabled {
			continue
		}
		payload, err := plugin.DecodeResourcePayload(definition.Spec, definition.ID)
		if err != nil {
			return err
		}
		for _, binary := range payload.Binaries {
			tools = append(tools, toolinstall.Tool{
				Key: binary.Tool, Version: binary.Version, Options: binary.Options,
				Lookup: toolinstall.LookupName(binary.Name, binary.Options), PublicName: binary.Name,
			})
		}
	}
	return toolinstall.Warm(ctx, home, tools)
}
