package manifestplugins

import (
	_ "embed"
	"errors"
	"os"

	"gopkg.in/yaml.v3"
)

//go:embed builtin_plugins.yaml
var builtinYAML []byte

func LoadBuiltin() (*Manifest, error) {
	rm, err := parseRawYAML(builtinYAML)
	if err != nil {
		return nil, err
	}
	return rawToManifest(rm), nil
}

func LoadUser(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Manifest{}, nil
		}
		return nil, err
	}
	rm, err := parseRawYAML(data)
	if err != nil {
		return nil, err
	}
	return rawToManifest(rm), nil
}

func parseRawYAML(data []byte) (rawManifest, error) {
	var rm rawManifest
	if err := yaml.Unmarshal(data, &rm); err != nil {
		return rawManifest{}, err
	}
	return rm, nil
}

func rawToManifest(rm rawManifest) *Manifest {
	m := &Manifest{Plugins: make([]ManifestPlugin, 0, len(rm.Plugins))}
	for _, rp := range rm.Plugins {
		m.Plugins = append(m.Plugins, resolvePlugin(rp, nil))
	}
	return m
}

func resolvePlugin(rp rawManifestPlugin, fallbackEnabled *bool) ManifestPlugin {
	enabled := false
	if rp.Enabled != nil {
		enabled = *rp.Enabled
	} else if fallbackEnabled != nil {
		enabled = *fallbackEnabled
	}
	return ManifestPlugin{
		ID:          rp.ID,
		Kind:        rp.Kind,
		Name:        rp.Name,
		DisplayName: rp.DisplayName,
		Description: rp.Description,
		Enabled:     enabled,
		Binaries:    rp.Binaries,
		Skills:      rp.Skills,
		SessionEnvs: rp.SessionEnvs,
	}
}

// MergeRaw merges parsed raw manifests, preserving nil Enabled for correct inheritance.
func MergeRaw(builtin, user rawManifest) *Manifest {
	userIndex := make(map[string]rawManifestPlugin, len(user.Plugins))
	for _, rp := range user.Plugins {
		userIndex[rp.ID] = rp
	}

	seenIDs := make(map[string]struct{}, len(builtin.Plugins)+len(user.Plugins))
	out := &Manifest{Plugins: make([]ManifestPlugin, 0, len(builtin.Plugins)+len(user.Plugins))}

	for _, bp := range builtin.Plugins {
		seenIDs[bp.ID] = struct{}{}
		if up, ok := userIndex[bp.ID]; ok {
			// Inherit builtin's Enabled when user doesn't specify.
			out.Plugins = append(out.Plugins, resolvePlugin(up, bp.Enabled))
		} else {
			out.Plugins = append(out.Plugins, resolvePlugin(bp, nil))
		}
	}

	for _, up := range user.Plugins {
		if _, seen := seenIDs[up.ID]; !seen {
			out.Plugins = append(out.Plugins, resolvePlugin(up, nil))
		}
	}

	return out
}

// Merge merges two Manifest values. Because Manifest.Enabled is bool (not *bool),
// user entries that omit enabled are treated as enabled=false. To preserve nil
// semantics, parse user YAML with LoadUserRaw and use MergeRaw.
func Merge(builtin, user *Manifest) *Manifest {
	builtinRaw := manifestToRaw(builtin)
	// For user plugins converted from Manifest, enabled is always explicit.
	userRaw := manifestToRaw(user)
	return MergeRaw(builtinRaw, userRaw)
}

func manifestToRaw(m *Manifest) rawManifest {
	rm := rawManifest{Plugins: make([]rawManifestPlugin, 0, len(m.Plugins))}
	for _, p := range m.Plugins {
		enabled := p.Enabled
		rm.Plugins = append(rm.Plugins, rawManifestPlugin{
			ID:          p.ID,
			Kind:        p.Kind,
			Name:        p.Name,
			DisplayName: p.DisplayName,
			Description: p.Description,
			Enabled:     &enabled,
			Binaries:    p.Binaries,
			Skills:      p.Skills,
			SessionEnvs: p.SessionEnvs,
		})
	}
	return rm
}
