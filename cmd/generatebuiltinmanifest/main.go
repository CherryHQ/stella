package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/CherryHQ/stella/internal/plugin/manifest"
	builtinplugins "github.com/CherryHQ/stella/plugins"
	"github.com/CherryHQ/stella/resources"
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
	oauthProviderIDs, err := manifest.BuiltinOAuthProviderIDs(resources.BuiltinOAuthYAML())
	if err != nil {
		fatal(err)
	}
	builtinManifest, err := manifest.GenerateBuiltinPlugins(filepath.Join(root, "plugins"), nil, oauthProviderIDs)
	if err != nil {
		fatal(err)
	}
	owners := make(map[string]struct{}, len(builtinManifest.Plugins))
	for _, plugin := range builtinManifest.Plugins {
		owners[plugin.ID] = struct{}{}
	}
	if err := validateBuiltinSkillDeclarations(assets, builtinManifest.Plugins); err != nil {
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
	if err := manifest.WriteBuiltinPlugins(filepath.Join(root, "plugins"), filepath.Join(root, "resources", "builtin_plugins_gen.go"), nil, oauthProviderIDs); err != nil {
		fatal(err)
	}
	if err := writeSystemRuntimes(filepath.Join(root, "plugins", "system", "runtime_gen.go"), builtinManifest); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "generate builtin manifest:", err)
	os.Exit(1)
}

func validateBuiltinSkillDeclarations(assets []builtinplugins.BuiltinSkillAsset, plugins []manifest.ManifestPlugin) error {
	expected := make(map[string][]string)
	for _, asset := range assets {
		if asset.OwnerPluginID != "" {
			expected[asset.OwnerPluginID] = append(expected[asset.OwnerPluginID], asset.Name)
		}
	}
	for owner := range expected {
		sort.Strings(expected[owner])
	}
	for _, plugin := range plugins {
		if _, ok := expected[plugin.ID]; !ok && len(plugin.Skills) == 0 {
			continue
		}
		if err := manifest.ValidateBundledSkillNames(plugin.Skills, expected[plugin.ID]); err != nil {
			return fmt.Errorf("builtin plugin %q skill declarations: %w", plugin.ID, err)
		}
		delete(expected, plugin.ID)
	}
	return nil
}
