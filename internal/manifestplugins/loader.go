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
	m := &Manifest{
		OAuthProviders: make([]ManifestOAuthProvider, 0, len(rm.OAuthProviders)),
		Plugins:        make([]ManifestPlugin, 0, len(rm.Plugins)),
	}
	for _, ro := range rm.OAuthProviders {
		m.OAuthProviders = append(m.OAuthProviders, resolveOAuthProvider(ro))
	}
	for _, rp := range rm.Plugins {
		m.Plugins = append(m.Plugins, resolvePlugin(rp, nil))
	}
	return m
}

func resolveOAuthProvider(ro rawManifestOAuthProvider) ManifestOAuthProvider {
	flows := make([]ManifestOAuthFlow, 0, len(ro.Flows))
	for _, rf := range ro.Flows {
		flows = append(flows, ManifestOAuthFlow(rf))
	}
	return ManifestOAuthProvider{
		ID:       ro.ID,
		Scopes:   ro.Scopes,
		VaultKey: ro.VaultKey,
		Flows:    flows,
	}
}

func resolvePlugin(rp rawManifestPlugin, fallbackEnabled *bool) ManifestPlugin {
	enabled := false
	if rp.Enabled != nil {
		enabled = *rp.Enabled
	} else if fallbackEnabled != nil {
		enabled = *fallbackEnabled
	}
	return ManifestPlugin{
		ID:                       rp.ID,
		Kind:                     rp.Kind,
		Name:                     rp.Name,
		DisplayName:              rp.DisplayName,
		Description:              rp.Description,
		Enabled:                  enabled,
		Prompt:                   rp.Prompt,
		ConfigDefaults:           rp.ConfigDefaults,
		ConfigSchema:             rp.ConfigSchema,
		ConfigSecretFields:       append([]string(nil), rp.ConfigSecretFields...),
		Binaries:                 rp.Binaries,
		Skills:                   rp.Skills,
		SessionEnvs:              rp.SessionEnvs,
		OAuthProvider:            rp.OAuthProvider,
		OAuthProviderConfigField: rp.OAuthProviderConfigField,
		OAuthProviderChoices:     append([]string(nil), rp.OAuthProviderChoices...),
	}
}

// MergeRaw merges parsed raw manifests, preserving nil Enabled for correct inheritance.
func MergeRaw(builtin, user rawManifest) *Manifest {
	userIndex := make(map[string]rawManifestPlugin, len(user.Plugins))
	for _, rp := range user.Plugins {
		userIndex[rp.ID] = rp
	}

	seenIDs := make(map[string]struct{}, len(builtin.Plugins)+len(user.Plugins))
	out := &Manifest{
		OAuthProviders: make([]ManifestOAuthProvider, 0, len(builtin.OAuthProviders)),
		Plugins:        make([]ManifestPlugin, 0, len(builtin.Plugins)+len(user.Plugins)),
	}

	// OAuth providers are not user-overridable; always take builtin.
	for _, ro := range builtin.OAuthProviders {
		out.OAuthProviders = append(out.OAuthProviders, resolveOAuthProvider(ro))
	}

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
	rm := rawManifest{
		OAuthProviders: make([]rawManifestOAuthProvider, 0, len(m.OAuthProviders)),
		Plugins:        make([]rawManifestPlugin, 0, len(m.Plugins)),
	}
	for _, o := range m.OAuthProviders {
		flows := make([]rawManifestOAuthFlow, 0, len(o.Flows))
		for _, f := range o.Flows {
			flows = append(flows, rawManifestOAuthFlow(f))
		}
		rm.OAuthProviders = append(rm.OAuthProviders, rawManifestOAuthProvider{
			ID:       o.ID,
			Scopes:   o.Scopes,
			VaultKey: o.VaultKey,
			Flows:    flows,
		})
	}
	for _, p := range m.Plugins {
		enabled := p.Enabled
		rm.Plugins = append(rm.Plugins, rawManifestPlugin{
			ID:                       p.ID,
			Kind:                     p.Kind,
			Name:                     p.Name,
			DisplayName:              p.DisplayName,
			Description:              p.Description,
			Enabled:                  &enabled,
			Prompt:                   p.Prompt,
			ConfigDefaults:           p.ConfigDefaults,
			ConfigSchema:             p.ConfigSchema,
			ConfigSecretFields:       append([]string(nil), p.ConfigSecretFields...),
			Binaries:                 p.Binaries,
			Skills:                   p.Skills,
			SessionEnvs:              p.SessionEnvs,
			OAuthProvider:            p.OAuthProvider,
			OAuthProviderConfigField: p.OAuthProviderConfigField,
			OAuthProviderChoices:     append([]string(nil), p.OAuthProviderChoices...),
		})
	}
	return rm
}
