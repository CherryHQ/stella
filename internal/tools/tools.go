package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// BinDir returns the tools binary directory path within annaHome.
func BinDir(annaHome string) string {
	return filepath.Join(annaHome, "bin")
}

// binaryFileName returns the platform-appropriate binary file name (appends .exe on Windows).
func binaryFileName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// ToolPath returns the full path to a named downloadable tool, or empty if not installed.
func ToolPath(annaHome, name string) string {
	p := filepath.Join(BinDir(annaHome), binaryFileName(name))
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

func specToTool(spec pkgplugins.BinarySpec) Tool {
	templates := make(map[string]AssetTemplate, len(spec.AssetTemplates))
	for k, v := range spec.AssetTemplates {
		templates[k] = AssetTemplate{File: v.File, RawBinary: v.RawBinary}
	}
	return Tool{
		Name:           spec.Name,
		Repo:           spec.Repo,
		Version:        spec.Version,
		AssetTemplates: templates,
	}
}

// DeduplicateByName returns one spec per binary name, sorted by (Name, PluginID)
// for deterministic output. When two specs share a name but differ in version,
// a warning is logged and the first (alphabetically by PluginID) is kept.
func DeduplicateByName(specs []pkgplugins.BinarySpec, logger *slog.Logger) []pkgplugins.BinarySpec {
	if logger == nil {
		logger = slog.Default()
	}
	sorted := append([]pkgplugins.BinarySpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].PluginID < sorted[j].PluginID
	})
	seen := map[string]pkgplugins.BinarySpec{}
	out := make([]pkgplugins.BinarySpec, 0, len(sorted))
	for _, s := range sorted {
		if existing, ok := seen[s.Name]; ok {
			if existing.Version != s.Version {
				logger.Warn("duplicate binary with conflicting versions, keeping first",
					"binary", s.Name,
					"kept_plugin", existing.PluginID, "kept_version", existing.Version,
					"skipped_plugin", s.PluginID, "skipped_version", s.Version)
			}
			continue
		}
		seen[s.Name] = s
		out = append(out, s)
	}
	return out
}

// EnsurePluginBinaries downloads each unique binary in specs that isn't already at
// the right version. Each binary downloads in its own goroutine; PostInstall runs
// immediately after its binary is ready. Specs are deduplicated by name before
// dispatching so the same binary never has two concurrent downloads.
func EnsurePluginBinaries(ctx context.Context, specs []pkgplugins.BinarySpec, annaHome string, logger *slog.Logger) {
	if len(specs) == 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	binDir := BinDir(annaHome)
	for _, spec := range DeduplicateByName(specs, logger) {
		go func() {
			bg := context.Background()
			tool := specToTool(spec)
			version := spec.Version
			if version == "" {
				v, err := FetchLatestVersion(bg, &tool)
				if err != nil {
					logger.Error("failed to fetch latest version", "binary", spec.Name, "error", err)
					return
				}
				version = v
			}
			if m, _ := LoadManifest(binDir); m != nil {
				if inst, ok := m.Tools[spec.Name]; ok && inst.Version != version {
					logger.Warn("binary version conflict: installed version differs from requested",
						"binary", spec.Name, "installed", inst.Version, "requested", version, "plugin", spec.PluginID)
				}
			}
			if err := DownloadVersion(bg, &tool, version, binDir, Platform()); err != nil {
				logger.Error("failed to download plugin binary", "plugin", spec.PluginID, "binary", spec.Name, "error", err)
				return
			}
			logger.Info("plugin binary downloaded", "plugin", spec.PluginID, "binary", spec.Name, "version", version)
			if spec.PostInstall != nil {
				spec.PostInstall(bg, filepath.Join(binDir, binaryFileName(spec.Name)), annaHome, logger)
			}
		}()
	}
}

// InstallBinarySpec downloads the binary declared by spec synchronously and runs PostInstall.
// Intended for CLI install commands where immediate feedback and error reporting are needed.
func InstallBinarySpec(ctx context.Context, spec pkgplugins.BinarySpec, annaHome string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	binDir := BinDir(annaHome)
	tool := specToTool(spec)
	version := spec.Version
	if version == "" {
		v, err := FetchLatestVersion(ctx, &tool)
		if err != nil {
			return fmt.Errorf("fetch latest version for %s: %w", spec.Name, err)
		}
		version = v
	}
	if m, _ := LoadManifest(binDir); m != nil {
		if inst, ok := m.Tools[spec.Name]; ok && inst.Version != version {
			logger.Warn("binary version conflict: installed version differs from requested",
				"binary", spec.Name, "installed", inst.Version, "requested", version, "plugin", spec.PluginID)
		}
	}
	if err := DownloadVersion(ctx, &tool, version, binDir, Platform()); err != nil {
		return err
	}
	if spec.PostInstall != nil {
		spec.PostInstall(ctx, filepath.Join(binDir, binaryFileName(spec.Name)), annaHome, logger)
	}
	return nil
}

// UpgradeBinarySpec upgrades the binary declared by spec to the latest GitHub release
// synchronously and runs PostInstall. Falls back to the pinned version when the
// latest-release API call fails and a pinned version is available.
func UpgradeBinarySpec(ctx context.Context, spec pkgplugins.BinarySpec, annaHome string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	binDir := BinDir(annaHome)
	tool := specToTool(spec)
	latest, err := FetchLatestVersion(ctx, &tool)
	if err != nil {
		if spec.Version != "" {
			logger.Warn("failed to fetch latest version, using pinned version", "binary", spec.Name, "error", err)
			latest = spec.Version
		} else {
			return fmt.Errorf("fetch latest version for %s: %w", spec.Name, err)
		}
	}
	if err := DownloadVersion(ctx, &tool, latest, binDir, Platform()); err != nil {
		return err
	}
	if spec.PostInstall != nil {
		spec.PostInstall(ctx, filepath.Join(binDir, binaryFileName(spec.Name)), annaHome, logger)
	}
	return nil
}

// StatusFromSpecs returns install status for each unique binary name in specs.
func StatusFromSpecs(specs []pkgplugins.BinarySpec, binDir string) []ToolStatus {
	manifest, _ := LoadManifest(binDir)
	seen := map[string]struct{}{}
	out := make([]ToolStatus, 0, len(specs))
	for _, spec := range specs {
		if _, ok := seen[spec.Name]; ok {
			continue
		}
		seen[spec.Name] = struct{}{}
		ts := ToolStatus{Name: spec.Name, Version: spec.Version}
		if ts.Version == "" {
			ts.Version = "latest"
		}
		if manifest != nil {
			if inst, ok := manifest.Tools[spec.Name]; ok {
				ts.Installed = true
				ts.Current = spec.Version == "" || inst.Version == spec.Version
			}
		}
		out = append(out, ts)
	}
	return out
}
