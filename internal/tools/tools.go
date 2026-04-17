package tools

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// BinDir returns the tools binary directory path within annaHome.
func BinDir(annaHome string) string {
	return filepath.Join(annaHome, "bin")
}

// ToolPath returns the full path to a named downloadable tool, or empty if not installed.
func ToolPath(annaHome, name string) string {
	p := filepath.Join(BinDir(annaHome), name)
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

// EnsurePluginBinaries downloads each binary in specs that isn't already at the
// right version. Each binary downloads in its own goroutine; PostInstall runs
// immediately after its binary is ready.
func EnsurePluginBinaries(ctx context.Context, specs []pkgplugins.BinarySpec, annaHome string, logger *slog.Logger) {
	if len(specs) == 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	binDir := BinDir(annaHome)
	for _, spec := range specs {
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
				spec.PostInstall(bg, filepath.Join(binDir, spec.Name), annaHome, logger)
			}
		}()
	}
}

// RunPostInstalls runs PostInstall hooks for specs whose binary is already present.
// Used at startup to refresh plugin assets without re-downloading.
func RunPostInstalls(_ context.Context, specs []pkgplugins.BinarySpec, annaHome string, logger *slog.Logger) {
	if len(specs) == 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	for _, spec := range specs {
		if spec.PostInstall == nil {
			continue
		}

		binPath := ToolPath(annaHome, spec.Name)
		if binPath == "" {
			continue
		}
		go spec.PostInstall(context.Background(), binPath, annaHome, logger)
	}
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
