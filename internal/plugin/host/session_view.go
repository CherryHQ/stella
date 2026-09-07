package host

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// SessionPluginView projects one already-authorized plugin snapshot for a
// runner. The snapshot is the only source of selected config and enabled state.
func (h *Host) SessionPluginView(snapshot plugin.Snapshot) (pkgplugins.SessionPluginView, error) {
	definitions := snapshot.Definitions()
	view := pkgplugins.SessionPluginView{
		RegisteredPluginIDs: make([]string, 0, len(definitions)),
	}
	for _, definition := range definitions {
		view.RegisteredPluginIDs = append(view.RegisteredPluginIDs, definition.ID)

		resolved, ok := snapshot.Get(definition.ID)
		if !ok {
			return pkgplugins.SessionPluginView{}, fmt.Errorf("resolve plugin %q", definition.ID)
		}
		if !resolved.Effective.IsEffectivelyEnabled {
			continue
		}

		identity, err := selectedResourceIdentity(definition, resolved)
		if err != nil {
			return pkgplugins.SessionPluginView{}, err
		}
		view.ExposedPluginIDs = append(view.ExposedPluginIDs, definition.ID)

		if err := validateResolvedCLIPayload(definition, resolved); err != nil {
			return pkgplugins.SessionPluginView{}, err
		}
		payload, err := manifest.DecodeCLIPayload(resolved.Effective.Payload, "selected resource payload")
		if err != nil {
			return pkgplugins.SessionPluginView{}, fmt.Errorf("plugin %q: %w", definition.ID, err)
		}
		appendCLIResources(&view, identity, payload)
	}

	slices.Sort(view.RegisteredPluginIDs)
	slices.Sort(view.ExposedPluginIDs)
	slices.SortFunc(view.SessionEnvSpecs, func(left, right pkgplugins.SessionEnvSpec) int {
		if left.EnvVar != right.EnvVar {
			return cmp.Compare(left.EnvVar, right.EnvVar)
		}
		if left.PluginID != right.PluginID {
			return cmp.Compare(left.PluginID, right.PluginID)
		}
		return cmp.Compare(left.ConfigID, right.ConfigID)
	})
	for i := 1; i < len(view.SessionEnvSpecs); i++ {
		previous, current := view.SessionEnvSpecs[i-1], view.SessionEnvSpecs[i]
		if previous.EnvVar == current.EnvVar {
			return pkgplugins.SessionPluginView{}, fmt.Errorf("session environment %q is declared by both %q and %q", current.EnvVar, previous.PluginID, current.PluginID)
		}
	}
	slices.SortFunc(view.BinarySpecs, func(left, right pkgplugins.PluginBinarySpec) int {
		if left.PluginID != right.PluginID {
			return cmp.Compare(left.PluginID, right.PluginID)
		}
		if left.Name != right.Name {
			return cmp.Compare(left.Name, right.Name)
		}
		return cmp.Compare(left.ConfigID, right.ConfigID)
	})
	return view, nil
}

// validateResolvedCLIPayload re-runs the backend boundary after resolution.
// A config saved while disabled may be structurally valid but incomplete; a
// capability lift must not turn that dormant payload into an executable one.
func validateResolvedCLIPayload(definition plugin.Definition, resolved plugin.ResolvedPlugin) error {
	if resolved.Config == nil {
		return fmt.Errorf("plugin %q is enabled without a selected config", definition.ID)
	}
	config := *resolved.Config
	enabled := true
	config.Enabled = &enabled
	config.Payload = resolved.Effective.Payload
	if err := manifest.ValidatePayload(context.Background(), definition, config, nil); err != nil {
		return fmt.Errorf("validate selected CLI payload for plugin %q: %w", definition.ID, err)
	}
	return nil
}

func selectedResourceIdentity(definition plugin.Definition, resolved plugin.ResolvedPlugin) (pkgplugins.PluginResourceIdentity, error) {
	if resolved.Config == nil {
		return pkgplugins.PluginResourceIdentity{}, fmt.Errorf("plugin %q is enabled without a selected config", definition.ID)
	}
	return pkgplugins.PluginResourceIdentity{
		PluginID: definition.ID,
		ConfigID: resolved.Config.ID,
		Scope:    string(resolved.Config.Scope),
		Revision: resolved.Config.Revision,
	}, nil
}

func appendCLIResources(view *pkgplugins.SessionPluginView, identity pkgplugins.PluginResourceIdentity, payload manifest.CLIPayload) {
	oauthScopes := make(map[string][]string, len(payload.OAuth))
	for _, requirement := range payload.OAuth {
		oauthScopes[requirement.Provider] = append(oauthScopes[requirement.Provider], requirement.Scopes...)
	}
	for _, binary := range payload.Binaries {
		view.BinarySpecs = append(view.BinarySpecs, pkgplugins.PluginBinarySpec{
			PluginResourceIdentity: identity,
			Name:                   binary.Name,
			Tool:                   binary.Tool,
			Version:                binary.Version,
			Options:                cloneMap(binary.Options),
		})
	}
	declaredEnv := make(map[string]struct{}, len(payload.SessionEnvs))
	for _, env := range payload.SessionEnvs {
		provider := payload.OAuthProvider
		for _, requirement := range payload.OAuth {
			for _, binding := range requirement.Bindings {
				if binding.EnvVar == env.EnvVar {
					provider = requirement.Provider
				}
			}
		}
		declaredEnv[env.EnvVar] = struct{}{}
		view.SessionEnvSpecs = append(view.SessionEnvSpecs, pkgplugins.SessionEnvSpec{
			PluginID: identity.PluginID, ConfigID: identity.ConfigID, Scope: identity.Scope, Revision: identity.Revision,
			EnvVar: env.EnvVar, Source: pkgplugins.SessionEnvSource(env.Source), Value: env.Value, Required: env.Required,
			OAuthProviderID: provider, OAuthScopes: slices.Clone(oauthScopes[provider]),
		})
	}
	for _, requirement := range payload.OAuth {
		for _, binding := range requirement.Bindings {
			// Only environment bindings belong in the sandbox projection.
			if binding.EnvVar == "" {
				continue
			}
			if _, declared := declaredEnv[binding.EnvVar]; declared {
				continue
			}
			view.SessionEnvSpecs = append(view.SessionEnvSpecs, pkgplugins.SessionEnvSpec{
				PluginID: identity.PluginID, ConfigID: identity.ConfigID, Scope: identity.Scope, Revision: identity.Revision,
				EnvVar: binding.EnvVar, Source: pkgplugins.SessionEnvSource("oauth." + binding.Credential), Required: true,
				OAuthProviderID: requirement.Provider, OAuthScopes: slices.Clone(oauthScopes[requirement.Provider]),
			})
		}
	}
}
