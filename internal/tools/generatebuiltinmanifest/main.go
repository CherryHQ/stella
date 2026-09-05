package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/CherryHQ/stella/internal/plugin/host"
	_ "github.com/CherryHQ/stella/internal/plugin/host/catalogimports"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/resources"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	catalog := host.New(nil)
	if err := catalog.LoadDefaultCatalog(); err != nil {
		fatal(fmt.Errorf("load default plugin catalog: %w", err))
	}
	specs := catalog.AllBundledSkillSpecs()
	if err := syncBundledSkills(context.Background(), root, specs); err != nil {
		fatal(err)
	}
	knownOwners, err := knownPluginIDs(catalog)
	if err != nil {
		fatal(err)
	}
	if err := resources.WriteBuiltinManifest(filepath.Join(root, "resources", "skills"), filepath.Join(root, "resources", "builtin_manifest_gen.go"), knownOwners); err != nil {
		fatal(err)
	}
}

func knownPluginIDs(catalog *host.Host) (map[string]struct{}, error) {
	known := make(map[string]struct{}, len(catalog.RegisteredPluginIDs()))
	for _, id := range catalog.RegisteredPluginIDs() {
		known[id] = struct{}{}
	}
	builtin, err := manifest.LoadBuiltin()
	if err != nil {
		return nil, fmt.Errorf("load builtin plugin manifest: %w", err)
	}
	for _, plugin := range builtin.Plugins {
		known[plugin.ID] = struct{}{}
	}
	return known, nil
}

func syncBundledSkills(ctx context.Context, root string, specs []pkgplugins.BundledSkillSpec) error {
	build := pkgplugins.BundledSkillSyncContext{WorkDir: root, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	for _, spec := range specs {
		if err := spec.Sync(ctx, build); err != nil {
			return fmt.Errorf("sync bundled skill %q from %q: %w", spec.Name, spec.PluginID, err)
		}
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "generate builtin manifest:", err)
	os.Exit(1)
}
