package manifest

import (
	"encoding/json"
	"fmt"
	"sync"

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

var builtinSystemPluginIDs struct {
	sync.Once
	ids map[string]struct{}
}

// IsSystemPlugin identifies a release-owned system CLI definition by the
// immutable shipped declaration. A bare canonical ID carries no installation
// mode information, so neither its spelling nor editable config can classify it.
func IsSystemPlugin(definition plugin.Definition) bool {
	if definition.Source != plugin.SourceBuiltin {
		return false
	}
	builtinSystemPluginIDs.Do(func() {
		builtinSystemPluginIDs.ids = make(map[string]struct{})
		builtin, err := LoadBuiltin()
		if err != nil {
			return
		}
		for _, authored := range builtin.Plugins {
			if authored.Kind == "system" {
				builtinSystemPluginIDs.ids[authored.ID] = struct{}{}
			}
		}
	})
	_, ok := builtinSystemPluginIDs.ids[definition.ID]
	return ok
}
