package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/CherryHQ/stella/internal/plugin/host"
	_ "github.com/CherryHQ/stella/internal/plugin/host/catalogimports"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/resources"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	if err := syncBundledSkills(context.Background(), root); err != nil {
		fatal(err)
	}
	if err := resources.WriteBuiltinManifest(filepath.Join(root, "resources", "skills"), filepath.Join(root, "resources", "builtin_manifest_gen.go")); err != nil {
		fatal(err)
	}
}

func syncBundledSkills(ctx context.Context, root string) error {
	specs, err := host.DefaultCatalogBundledSkillSpecs()
	if err != nil {
		return fmt.Errorf("load bundled skill catalog: %w", err)
	}
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
