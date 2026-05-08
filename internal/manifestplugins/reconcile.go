package manifestplugins

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"
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

// isCacheHit returns true if the state already records the binary at the given version.
// Returns false for empty version (latest) so mise always verifies the install.
func isCacheHit(state *ManifestState, pluginID, binaryName, version string) bool {
	if version == "" {
		return false
	}
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

// upsertBinaryState updates or appends a binary install record in state.
func upsertBinaryState(state *ManifestState, pluginID string, install BinaryInstallState) {
	ps := state.Plugins[pluginID]
	for i, b := range ps.Binaries {
		if b.Name == install.Name {
			ps.Binaries[i] = install
			state.Plugins[pluginID] = ps
			return
		}
	}
	ps.Binaries = append(ps.Binaries, install)
	state.Plugins[pluginID] = ps
}

// Reconcile processes all enabled plugins in the manifest, downloading any binaries
// that are not already at the correct version according to the state file.
func Reconcile(ctx context.Context, m *Manifest, stellaHome string) ReconcileResult {
	statePath := StatePath(stellaHome)
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

	// Ensure mise is available before processing any plugin binaries.
	if bootstrapErr := bootstrapMise(ctx, stellaHome); bootstrapErr != nil {
		slog.Error("manifest plugin reconcile: mise bootstrap failed", "error", bootstrapErr)
	}

	errorCount := 0

	for _, plugin := range m.Plugins {
		if !plugin.Enabled {
			continue
		}

		pr := PluginReconcileResult{PluginID: plugin.ID}

		for _, binary := range plugin.Binaries {
			if ctx.Err() != nil {
				slog.Info("manifest plugin reconcile aborted", "reason", ctx.Err())
				result.Plugins[plugin.ID] = pr
				goto done
			}

			// Cache hit check (skipped for latest/empty version)
			if isCacheHit(state, plugin.ID, binary.Name, binary.Version) {
				slog.Info("manifest binary cache hit",
					"plugin", plugin.ID,
					"binary", binary.Name,
					"version", binary.Version)
				pr.Binaries = append(pr.Binaries, BinaryReconcileResult{
					Name:     binary.Name,
					Version:  binary.Version,
					CacheHit: true,
				})
				continue
			}

			slog.Info("manifest binary installing",
				"plugin", plugin.ID,
				"binary", binary.Name,
				"version", binary.Version)

			installedVersion, installErr := installBinaryWithMise(ctx, binary, stellaHome)
			if installErr != nil {
				slog.Error("manifest binary install failed",
					"plugin", plugin.ID,
					"binary", binary.Name,
					"error", installErr)
				pr.Binaries = append(pr.Binaries, BinaryReconcileResult{
					Name:    binary.Name,
					Version: binary.Version,
					Err:     installErr,
				})
				errorCount++
				continue
			}

			slog.Info("manifest binary installed",
				"plugin", plugin.ID,
				"binary", binary.Name,
				"version", installedVersion)

			pr.Binaries = append(pr.Binaries, BinaryReconcileResult{
				Name:    binary.Name,
				Version: installedVersion,
			})

			upsertBinaryState(state, plugin.ID, BinaryInstallState{
				Name:        binary.Name,
				Tool:        binary.Tool,
				Version:     installedVersion,
				InstalledAt: time.Now(),
			})
		}

		result.Plugins[plugin.ID] = pr
	}

done:
	state.UpdatedAt = time.Now()
	if saveErr := SaveState(statePath, state); saveErr != nil {
		slog.Error("manifest plugin reconcile: failed to save state", "error", saveErr)
	}

	slog.Info("manifest plugin reconcile done",
		"enabled_plugins", enabledCount,
		"errors", errorCount)

	return result
}
