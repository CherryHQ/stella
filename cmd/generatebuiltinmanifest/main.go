package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/CherryHQ/stella/internal/plugin"
	builtinplugins "github.com/CherryHQ/stella/plugins"
	"github.com/CherryHQ/stella/resources"
	"github.com/CherryHQ/stella/resources/binaries"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	assets := builtinplugins.BuiltinSkillAssets()
	if len(assets) == 0 {
		fatal(fmt.Errorf("generated builtin asset table is empty; run generatepluginassets first"))
	}
	sources := make([]resources.BuiltinSkillSource, 0, len(assets))
	for _, asset := range assets {
		sources = append(sources, resources.BuiltinSkillSource{
			Name:          asset.Name,
			SourceRoot:    asset.SourceRoot,
			LogicalRoot:   asset.LogicalRoot,
			OwnerPluginID: asset.OwnerPluginID,
		})
	}
	providerIDs, err := parseOAuthProviderIDs(resources.BuiltinOAuthYAML())
	if err != nil {
		fatal(err)
	}
	catalog, err := generateBuiltinDefinitions(filepath.Join(root, "plugins"), binaries.KnownRuntimeNames(), providerIDs)
	if err != nil {
		fatal(err)
	}
	owners := make(map[string]struct{}, len(catalog))
	for _, definition := range catalog {
		owners[definition.ID] = struct{}{}
	}
	if err := validateBuiltinSkillDeclarations(assets, catalog); err != nil {
		fatal(err)
	}
	for _, asset := range assets {
		if asset.OwnerPluginID == "" {
			continue
		}
		if _, ok := owners[asset.OwnerPluginID]; !ok {
			fatal(fmt.Errorf("builtin skill %q has unknown owner %q", asset.Name, asset.OwnerPluginID))
		}
	}
	if err := resources.WriteBuiltinManifestFromAssets(root, filepath.Join(root, "resources", "builtin_manifest_gen.go"), sources); err != nil {
		fatal(err)
	}
	if err := writeBuiltinDefinitions(filepath.Join(root, "plugins"), filepath.Join(root, "resources", "builtin_plugins_gen.go"), binaries.KnownRuntimeNames(), providerIDs); err != nil {
		fatal(err)
	}
	if err := writeSystemRuntimes(filepath.Join(root, "plugins", "system", "runtime_gen.go"), catalog); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "generate builtin catalog:", err)
	os.Exit(1)
}

func validateBuiltinSkillDeclarations(assets []builtinplugins.BuiltinSkillAsset, definitions []builtinDefinition) error {
	expected := make(map[string][]string)
	for _, asset := range assets {
		if asset.OwnerPluginID != "" {
			expected[asset.OwnerPluginID] = append(expected[asset.OwnerPluginID], asset.Name)
		}
	}
	for owner := range expected {
		sort.Strings(expected[owner])
	}
	for _, definition := range definitions {
		payload, err := plugin.DecodeResourcePayload(definition.Spec, "plugin "+definition.ID)
		if err != nil {
			return err
		}
		if _, ok := expected[definition.ID]; !ok && len(payload.Skills) == 0 {
			continue
		}
		if err := plugin.ValidateBundledSkillNames(payload.Skills, expected[definition.ID]); err != nil {
			return fmt.Errorf("builtin plugin %q skill declarations: %w", definition.ID, err)
		}
		delete(expected, definition.ID)
	}
	if len(expected) != 0 {
		return fmt.Errorf("builtin skill declarations have unknown owners: %v", sortedKeys(expected))
	}
	return nil
}

func sortedKeys(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
