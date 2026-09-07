package manifest

import (
	"encoding/json"
	"fmt"

	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

// BuiltinDefinitions normalizes release assets before the catalog transaction.
// It deliberately loads the embedded input, never a resolved admin override.
func BuiltinDefinitions() ([]plugin.Definition, error) {
	manifest, err := LoadBuiltin()
	if err != nil {
		return nil, err
	}
	if err := Validate(manifest); err != nil {
		return nil, err
	}
	definitions := make([]plugin.Definition, 0, len(manifest.Plugins))
	for _, authored := range manifest.Plugins {
		if !agentpackage.ValidName(authored.Name) {
			return nil, fmt.Errorf("plugin %s: invalid canonical name", authored.Name)
		}
		spec, err := json.Marshal(cliPayload{
			Description: authored.Description, Category: authored.Category, Prompt: authored.Prompt,
			Binaries: authored.Binaries, Skills: authored.Skills, SessionEnvs: authored.SessionEnvs,
			OAuthProvider: authored.OAuthProvider, OAuth: authored.OAuth, MCPServers: authored.MCPServers,
		})
		if err != nil {
			return nil, fmt.Errorf("plugin %s: %w", authored.ID, err)
		}
		definition := plugin.Definition{
			ID: authored.Name, DisplayName: authored.DisplayName,
			Source:         plugin.SourceBuiltin,
			Spec:           spec,
			DefaultEnabled: authored.Enabled, Revision: 1,
		}
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("plugin %s: %w", authored.ID, err)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}
