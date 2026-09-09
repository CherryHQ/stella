package runtime

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

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
