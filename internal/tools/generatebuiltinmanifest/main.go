package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/CherryHQ/stella/internal/plugin/host"
	_ "github.com/CherryHQ/stella/internal/plugin/host/catalogimports"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	coreplugins "github.com/CherryHQ/stella/plugins/core"
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
	if err := syncCoreSkills(root); err != nil {
		fatal(err)
	}
	if err := resources.WriteBuiltinManifest(filepath.Join(root, "resources", "skills"), filepath.Join(root, "resources", "builtin_manifest_gen.go")); err != nil {
		fatal(err)
	}
	reservedRuntimeNames := make([]string, 0, len(coreplugins.RuntimeResources()))
	for _, resource := range coreplugins.RuntimeResources() {
		reservedRuntimeNames = append(reservedRuntimeNames, resource.Name)
	}
	oauthProviderIDs, err := manifest.BuiltinOAuthProviderIDs(resources.BuiltinOAuthYAML())
	if err != nil {
		fatal(err)
	}
	if err := manifest.WriteBuiltinPlugins(filepath.Join(root, "plugins"), filepath.Join(root, "resources", "builtin_plugins_gen.go"), reservedRuntimeNames, oauthProviderIDs); err != nil {
		fatal(err)
	}
}

func syncCoreSkills(root string) error {
	sourceRoot := filepath.Join(root, "plugins", "core", "skills")
	source := os.DirFS(sourceRoot)
	destination := filepath.Join(root, "resources", "skills", "core")
	parent := filepath.Dir(destination)
	stage, err := os.MkdirTemp(parent, ".core-skills-")
	if err != nil {
		return fmt.Errorf("create core skill staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := copyCoreSkillTree(source, stage); err != nil {
		return err
	}
	return publishCoreSkillMirror(destination, stage)
}

func publishCoreSkillMirror(destination, stage string) error {
	return publishCoreSkillMirrorWithOps(destination, stage, os.Rename, os.RemoveAll)
}

func publishCoreSkillMirrorWithOps(destination, stage string, rename func(string, string) error, removeAll func(string) error) error {
	parent := filepath.Dir(destination)
	backupFile, err := os.CreateTemp(parent, ".core-skills-backup-")
	if err != nil {
		return fmt.Errorf("create core skill backup: %w", err)
	}
	backup := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		return fmt.Errorf("close core skill backup: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("prepare core skill backup: %w", err)
	}
	hadDestination := false
	if _, err := os.Lstat(destination); err == nil {
		if err := rename(destination, backup); err != nil {
			return fmt.Errorf("stage existing core skill mirror: %w", err)
		}
		hadDestination = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing core skill mirror: %w", err)
	}
	if err := rename(stage, destination); err != nil {
		if hadDestination {
			_ = rename(backup, destination)
		}
		return fmt.Errorf("publish generated core skill mirror: %w", err)
	}
	if hadDestination {
		if err := removeAll(backup); err != nil {
			return fmt.Errorf("remove old core skill mirror: %w", err)
		}
	}
	return nil
}

func copyCoreSkillTree(source fs.FS, destination string) error {
	return fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(".", filepath.FromSlash(name))
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("core skill source contains symlink %q", name)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("core skill source contains unsupported file %q", name)
		}
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return fmt.Errorf("read core skill source %q: %w", name, err)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write core skill mirror %q: %w", rel, err)
		}
		return nil
	})
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
