package runtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const packageConfigUnavailableReason = "selected package configuration is incompatible"

// projectSessionPluginView derives resources and prompts together from the
// authority-bound snapshot captured when the runner is admitted.
func projectSessionPluginView(snapshot plugin.Snapshot) (pkgplugins.SessionPluginView, error) {
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

		payloadSource := resolved.Effective.Payload
		payloadName := "selected resource payload"
		unavailableReason := ""
		if err := validateResolvedResourcePayload(definition, resolved); err != nil {
			if !errors.Is(err, plugin.ErrInvalidConfig) {
				return pkgplugins.SessionPluginView{}, err
			}
			// A published definition can advance independently of a user's saved
			// CLI/MCP pin. Keep the current declaration in the immutable view so
			// global conflict checks and Skill masking still see it, then mark the
			// whole package unavailable. No package resource survives the readiness
			// projection below.
			payloadSource = definition.Spec
			payloadName = "published resource payload"
			unavailableReason = packageConfigUnavailableReason
		}
		payload, err := plugin.DecodeResourcePayload(payloadSource, payloadName)
		if err != nil {
			return pkgplugins.SessionPluginView{}, fmt.Errorf("plugin %q: %w", definition.ID, err)
		}
		view.PackageRequirements = append(view.PackageRequirements, packageRequirement(definition.ID, payload.OAuth, unavailableReason))
		view.PackageResults.Packages = append(view.PackageResults.Packages, pkgplugins.PluginPackageStatus{
			PluginID: definition.ID,
			Ready:    unavailableReason == "",
			Reason:   unavailableReason,
		})
		appendCLIResources(&view, identity, payload)
		appendSkillResources(&view, identity, payload, definition.Source == plugin.SourceBuiltin)
		if payload.Prompt != "" {
			view.PromptSections = append(view.PromptSections, pkgplugins.SystemPromptSection{PluginID: definition.ID, Title: definition.DisplayName, Content: payload.Prompt, Inline: true})
		}
	}

	slices.Sort(view.RegisteredPluginIDs)
	slices.Sort(view.ExposedPluginIDs)
	slices.SortFunc(view.PackageResults.Packages, func(left, right pkgplugins.PluginPackageStatus) int {
		return cmp.Compare(left.PluginID, right.PluginID)
	})
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
	slices.SortFunc(view.SkillSpecs, func(left, right pkgplugins.PluginSkillSpec) int {
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

// projectFileSessionPluginView projects already-captured resources without
// manufacturing Definition or Config rows. Every identity is the scoped file
// key, so same names in different owners remain distinct.
func projectFileSessionPluginView(resources []plugin.FileResource) (pkgplugins.SessionPluginView, error) {
	view := pkgplugins.SessionPluginView{
		RegisteredPluginIDs: make([]string, 0, len(resources)),
	}
	for _, resource := range resources {
		id := resource.Key.ID()
		if id == "" {
			return pkgplugins.SessionPluginView{}, fmt.Errorf("file resource %q has invalid identity", resource.Key.Name)
		}
		view.RegisteredPluginIDs = append(view.RegisteredPluginIDs, id)
		identity := pkgplugins.PluginResourceIdentity{PluginID: id, Scope: string(resource.Key.Scope)}
		if resource.Key.Kind == plugin.ResourceSkill {
			continue
		}
		if resource.Key.Kind == plugin.ResourceMCP {
			if !resource.Disabled && !resource.Forbidden && !fileResourceFatalDiagnostic(resource) {
				view.ExposedPluginIDs = append(view.ExposedPluginIDs, id)
			}
			appendFileMCPDirectory(&view, identity, resource)
			continue
		}
		if resource.Key.Kind != plugin.ResourcePlugin {
			continue
		}
		unavailable := fileResourcePackageUnavailable(resource)
		if unavailable {
			view.PackageResults.Packages = append(view.PackageResults.Packages, pkgplugins.PluginPackageStatus{PluginID: id, Reason: fileResourceUnavailableReason(resource)})
		} else {
			view.ExposedPluginIDs = append(view.ExposedPluginIDs, id)
		}
		if resource.Package == nil {
			if !unavailable {
				view.PackageResults.Packages = append(view.PackageResults.Packages, pkgplugins.PluginPackageStatus{PluginID: id, Reason: fileResourceUnavailableReason(resource)})
			}
			continue
		}
		payload, err := plugin.ResourcePayloadFromAgentPackage(resource.Package)
		if err != nil {
			return pkgplugins.SessionPluginView{}, fmt.Errorf("file plugin %q: %w", id, err)
		}
		if resource.Digest != "" {
			payload.ContentDigest = resource.Digest
			payload.Content = &plugin.ContentReference{Digest: resource.Digest}
		}
		if !unavailable {
			view.PackageResults.Packages = append(view.PackageResults.Packages, pkgplugins.PluginPackageStatus{PluginID: id, Ready: true})
		}
		reason := ""
		if unavailable {
			reason = fileResourceUnavailableReason(resource)
		}
		view.PackageRequirements = append(view.PackageRequirements, packageRequirement(id, payload.OAuth, reason))
		appendCLIResources(&view, identity, payload)
		appendSkillResources(&view, identity, payload, false)
		appendFileMCPDirectory(&view, identity, resource)
		if payload.Prompt != "" {
			view.PromptSections = append(view.PromptSections, pkgplugins.SystemPromptSection{PluginID: id, Title: resource.Package.Manifest.Name, Content: payload.Prompt, Inline: true})
		}
	}
	slices.Sort(view.RegisteredPluginIDs)
	slices.Sort(view.ExposedPluginIDs)
	slices.SortFunc(view.PackageResults.Packages, func(left, right pkgplugins.PluginPackageStatus) int {
		return cmp.Compare(left.PluginID, right.PluginID)
	})
	return view, nil
}

func appendFileMCPDirectory(view *pkgplugins.SessionPluginView, identity pkgplugins.PluginResourceIdentity, resource plugin.FileResource) {
	for serverKey := range resource.MCP {
		status := "declared"
		statusError := ""
		if resource.Disabled || resource.Forbidden || fileResourceFatalDiagnostic(resource) {
			status = "unavailable"
			statusError = fileResourceUnavailableReason(resource)
		}
		view.MCPDirectory = append(view.MCPDirectory, pkgplugins.MCPDirectoryEntry{
			PluginResourceIdentity: identity,
			ServerKey:              serverKey,
			Status:                 status,
			StatusError:            statusError,
		})
	}
}

func fileResourceUnavailableReason(resource plugin.FileResource) string {
	if resource.Forbidden {
		return "file resource is forbidden"
	}
	if resource.Disabled {
		return "file resource is disabled"
	}
	for _, diagnostic := range resource.Diagnostics {
		if diagnostic.Severity == agentpackage.SeverityError {
			if diagnostic.Message != "" {
				return diagnostic.Message
			}
			if diagnostic.Code != "" {
				return diagnostic.Code
			}
		}
	}
	return "file resource is unavailable"
}

func fileResourcePackageUnavailable(resource plugin.FileResource) bool {
	return resource.Disabled || resource.Forbidden || resource.Package == nil || fileResourceFatalDiagnostic(resource)
}

func fileResourceFatalDiagnostic(resource plugin.FileResource) bool {
	for _, diagnostic := range resource.Diagnostics {
		if diagnostic.Severity != agentpackage.SeverityError {
			continue
		}
		switch diagnostic.Code {
		case "resource.capture", "resource.requirement_conflict":
			return true
		}
	}
	return false
}

func packageRequirement(pluginID string, requirements []plugin.OAuthRequirement, unavailableReason string) pkgplugins.PluginPackageRequirement {
	result := pkgplugins.PluginPackageRequirement{PluginID: pluginID, UnavailableReason: unavailableReason, OAuth: make([]pkgplugins.PluginOAuthRequirement, 0, len(requirements))}
	for _, requirement := range requirements {
		converted := pkgplugins.PluginOAuthRequirement{Provider: requirement.Provider, Scopes: slices.Clone(requirement.Scopes), Bindings: make([]pkgplugins.PluginOAuthBinding, 0, len(requirement.Bindings))}
		for _, binding := range requirement.Bindings {
			converted.Bindings = append(converted.Bindings, pkgplugins.PluginOAuthBinding{Credential: binding.Credential, EnvVar: binding.EnvVar, Connection: binding.Connection})
		}
		result.OAuth = append(result.OAuth, converted)
	}
	return result
}

func appendSkillResources(view *pkgplugins.SessionPluginView, identity pkgplugins.PluginResourceIdentity, payload plugin.ResourcePayload, builtin bool) {
	packageDigest := payload.ContentDigest
	if payload.Content != nil && payload.Content.Digest != "" {
		// Skill bytes live below the published asset tree. The definition
		// digest identifies the declaration, but the content reference is the
		// immutable directory the package reader must open.
		packageDigest = payload.Content.Digest
	}
	for _, skill := range payload.Skills {
		path := skill.Path
		if path == "" {
			path = "skills/" + skill.Name + "/SKILL.md"
		}
		view.SkillSpecs = append(view.SkillSpecs, pkgplugins.PluginSkillSpec{
			PluginResourceIdentity: identity,
			PackageDigest:          packageDigest,
			Name:                   skill.Name,
			Path:                   path,
			Description:            skill.Description,
			Builtin:                builtin,
		})
	}
}

// validateResolvedResourcePayload re-runs the backend boundary after resolution.
// A config saved while disabled may be structurally valid but incomplete; a
// capability lift must not turn that dormant payload into an executable one.
func validateResolvedResourcePayload(definition plugin.Definition, resolved plugin.ResolvedPlugin) error {
	if resolved.Config == nil {
		return fmt.Errorf("plugin %q is enabled without a selected config", definition.ID)
	}
	config := *resolved.Config
	enabled := true
	config.Enabled = &enabled
	config.Payload = resolved.Effective.Payload
	if err := plugin.ValidatePayload(context.Background(), definition, config, nil); err != nil {
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

func appendCLIResources(view *pkgplugins.SessionPluginView, identity pkgplugins.PluginResourceIdentity, payload plugin.ResourcePayload) {
	oauthScopes := make(map[string][]string, len(payload.OAuth))
	for _, requirement := range payload.OAuth {
		oauthScopes[requirement.Provider] = append(oauthScopes[requirement.Provider], requirement.Scopes...)
	}
	for _, binary := range payload.Binaries {
		view.BinarySpecs = append(view.BinarySpecs, pkgplugins.PluginBinarySpec{
			PluginResourceIdentity: identity,
			PackageDigest:          payload.ContentDigest,
			Name:                   binary.Name,
			Tool:                   binary.Tool,
			Version:                binary.Version,
			Options:                clonePluginOptions(binary.Options),
		})
	}
	declaredEnv := make(map[string]struct{}, len(payload.SessionEnvs))
	for _, env := range payload.SessionEnvs {
		provider := ""
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
