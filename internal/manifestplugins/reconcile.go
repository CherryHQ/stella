package manifestplugins

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/vaayne/anna/internal/tools"
)

// ReconcileResult summarizes one reconcile run.
type ReconcileResult struct {
	EnabledCount int
	// Per-plugin results, keyed by plugin ID
	Plugins map[string]PluginReconcileResult
}

// PluginReconcileResult holds the result for a single plugin.
type PluginReconcileResult struct {
	PluginID string
	Binaries []BinaryReconcileResult
	// Skills not yet implemented (bundled-only in v1), but reserved for future use
	Err error
}

// BinaryReconcileResult holds the result for a single binary within a plugin.
type BinaryReconcileResult struct {
	Name     string
	Version  string
	CacheHit bool
	Err      error
}

// LoadState reads the manifest state file at path. If the file does not exist,
// an empty state is returned.
func LoadState(path string) (*ManifestState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &ManifestState{Plugins: make(map[string]PluginInstallState)}, nil
		}
		return nil, err
	}
	var s ManifestState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Plugins == nil {
		s.Plugins = make(map[string]PluginInstallState)
	}
	return &s, nil
}

// SaveState writes the manifest state to path atomically (write to .tmp then rename).
func SaveState(path string, s *ManifestState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// manifestBinaryToTool converts a ManifestBinary to a tools.Tool.
func manifestBinaryToTool(b ManifestBinary) tools.Tool {
	templates := make(map[string]tools.AssetTemplate, len(b.AssetTemplates))
	for platform, asset := range b.AssetTemplates {
		templates[platform] = tools.AssetTemplate{
			File:      asset.File,
			RawBinary: asset.RawBinary,
		}
	}
	return tools.Tool{
		Name:           b.Name,
		Repo:           b.Repo,
		Version:        b.Version,
		AssetTemplates: templates,
	}
}

// isCacheHit returns true if the state already records the binary at the resolved version.
func isCacheHit(state *ManifestState, pluginID, binaryName, version string) bool {
	ps, ok := state.Plugins[pluginID]
	if !ok {
		return false
	}
	for _, b := range ps.Binaries {
		if b.Name == binaryName && b.Version == version {
			return true
		}
	}
	return false
}

// Reconcile processes all enabled plugins in the manifest, downloading any binaries
// that are not already at the correct version according to the state file.
func Reconcile(ctx context.Context, m *Manifest, annaHome string) ReconcileResult {
	statePath := StatePath(annaHome)
	state, err := LoadState(statePath)
	if err != nil {
		slog.Error("manifest plugin reconcile: failed to load state", "error", err)
		state = &ManifestState{Plugins: make(map[string]PluginInstallState)}
	}

	enabledCount := 0
	for _, p := range m.Plugins {
		if p.Enabled {
			enabledCount++
		}
	}

	slog.Info("manifest plugin reconcile started", "enabled_plugins", enabledCount)

	result := ReconcileResult{
		EnabledCount: enabledCount,
		Plugins:      make(map[string]PluginReconcileResult),
	}

	// Track newly installed binaries: map[pluginID][]BinaryInstallState
	type successKey struct{ pluginID, binaryName string }
	successInstalls := make(map[successKey]BinaryInstallState)

	errorCount := 0

	for _, plugin := range m.Plugins {
		if !plugin.Enabled {
			continue
		}

		pr := PluginReconcileResult{PluginID: plugin.ID}

		for _, binary := range plugin.Binaries {
			tool := manifestBinaryToTool(binary)

			// Resolve version
			version := binary.Version
			if version == "" {
				v, fetchErr := tools.FetchLatestVersion(ctx, &tool)
				if fetchErr != nil {
					slog.Error("manifest binary install failed",
						"plugin", plugin.ID,
						"binary", binary.Name,
						"error", fetchErr)
					pr.Binaries = append(pr.Binaries, BinaryReconcileResult{
						Name: binary.Name,
						Err:  fetchErr,
					})
					errorCount++
					continue
				}
				version = v
			}

			// Cache hit check
			if isCacheHit(state, plugin.ID, binary.Name, version) {
				slog.Info("manifest binary cache hit",
					"plugin", plugin.ID,
					"binary", binary.Name,
					"version", version)
				pr.Binaries = append(pr.Binaries, BinaryReconcileResult{
					Name:     binary.Name,
					Version:  version,
					CacheHit: true,
				})
				continue
			}

			// Download
			slog.Info("manifest binary downloading",
				"plugin", plugin.ID,
				"binary", binary.Name,
				"version", version)

			binDir := tools.BinDir(annaHome)
			downloadErr := tools.DownloadVersion(ctx, &tool, version, binDir, tools.Platform())
			if downloadErr != nil {
				slog.Error("manifest binary install failed",
					"plugin", plugin.ID,
					"binary", binary.Name,
					"error", downloadErr)
				pr.Binaries = append(pr.Binaries, BinaryReconcileResult{
					Name:    binary.Name,
					Version: version,
					Err:     downloadErr,
				})
				errorCount++
				continue
			}

			slog.Info("manifest binary installed",
				"plugin", plugin.ID,
				"binary", binary.Name,
				"version", version)

			pr.Binaries = append(pr.Binaries, BinaryReconcileResult{
				Name:    binary.Name,
				Version: version,
			})

			successInstalls[successKey{plugin.ID, binary.Name}] = BinaryInstallState{
				Name:        binary.Name,
				Repo:        binary.Repo,
				Version:     version,
				InstalledAt: time.Now(),
			}
		}

		result.Plugins[plugin.ID] = pr
	}

	// Merge successful installs into state (preserve existing entries for untouched binaries).
	for key, installState := range successInstalls {
		ps := state.Plugins[key.pluginID]
		// Rebuild the binaries slice: replace matching entry or append.
		found := false
		for i, b := range ps.Binaries {
			if b.Name == key.binaryName {
				ps.Binaries[i] = installState
				found = true
				break
			}
		}
		if !found {
			ps.Binaries = append(ps.Binaries, installState)
		}
		state.Plugins[key.pluginID] = ps
	}

	state.UpdatedAt = time.Now()
	if saveErr := SaveState(statePath, state); saveErr != nil {
		slog.Error("manifest plugin reconcile: failed to save state", "error", saveErr)
	}

	slog.Info("manifest plugin reconcile done",
		"enabled_plugins", enabledCount,
		"errors", errorCount)

	return result
}
