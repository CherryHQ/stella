package server

import (
	"io/fs"
	"slices"
	"strings"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func pluginFileResourceView(resource plugin.FileResource, writable bool) (apitypes.PluginResource, error) {
	id := resource.Key.ID()
	contentDigest := resource.Digest
	settingsDigest := resource.SettingsDigest
	forbidden := resource.Forbidden
	readOnly := !writable
	overridden := resource.Overridden
	view := apitypes.PluginResource{
		Id:              &id,
		Name:            resource.Key.Name,
		Scope:           apitypes.PluginResourceScope(resource.Key.Scope),
		ContentDigest:   &contentDigest,
		SettingsDigest:  &settingsDigest,
		IsEnabled:       !resource.Disabled,
		IsForbidden:     &forbidden,
		IsReadOnly:      &readOnly,
		IsOverridden:    &overridden,
		Diagnostics:     pluginDiagnostics(resource.Diagnostics),
		Files:           pluginFileInfos(resource.Content),
		ResourceSummary: pluginPackageSummary(resource),
	}
	if resource.Key.UserID != "" {
		if userID, err := uuid.Parse(resource.Key.UserID); err == nil {
			view.UserId = &userID
		}
	}
	if resource.Key.AgentID != "" {
		view.AgentId = &resource.Key.AgentID
	}
	if resource.Package != nil {
		view.Version = resource.Package.Manifest.Version
		view.Description = resource.Package.Manifest.Description
		view.DisplayName = resource.Package.Manifest.Name
		if resource.Package.Extension != nil && resource.Package.Extension.DisplayName != "" {
			view.DisplayName = resource.Package.Extension.DisplayName
		}
	}
	return view, nil
}

func pluginDiagnostics(items agentpackage.Diagnostics) *[]apitypes.PluginDiagnostic {
	result := make([]apitypes.PluginDiagnostic, 0, len(items))
	for _, item := range items {
		severity := apitypes.PluginDiagnosticSeverity(item.Severity)
		path := item.Path
		var diagnosticPath *string
		if path != "" {
			diagnosticPath = &path
		}
		result = append(result, apitypes.PluginDiagnostic{Severity: severity, Code: item.Code, Message: item.Message, Path: diagnosticPath})
	}
	return &result
}

func pluginFileInfos(content *plugin.ResourceContent) *[]apitypes.ResourceFileInfo {
	result := make([]apitypes.ResourceFileInfo, 0)
	if content == nil {
		return &result
	}
	_ = fs.WalkDir(content.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		size := info.Size()
		result = append(result, apitypes.ResourceFileInfo{Path: name, Size: &size, IsExecutable: info.Mode().Perm()&0o111 != 0})
		return nil
	})
	slices.SortFunc(result, func(a, b apitypes.ResourceFileInfo) int { return strings.Compare(a.Path, b.Path) })
	return &result
}

func pluginPackageSummary(resource plugin.FileResource) apitypes.PluginResourceSummary {
	result := apitypes.PluginResourceSummary{
		Binaries:   []apitypes.PluginCLIBackendBinarySummary{},
		Skills:     []apitypes.PluginCLIBackendSkillSummary{},
		SessionEnv: []apitypes.PluginCLIBackendSessionEnvSummary{},
		McpServers: []apitypes.PluginMCPServerSummary{},
	}
	pkg := resource.Package
	if pkg == nil {
		return result
	}
	if pkg.Extension != nil {
		result.OauthProviderConfigured = len(pkg.Extension.OAuth) != 0
		for _, binary := range pkg.Extension.Binaries {
			result.Binaries = append(result.Binaries, apitypes.PluginCLIBackendBinarySummary{Name: binary.Name, Version: binary.Version})
		}
		for _, env := range pkg.Extension.SessionEnv {
			result.SessionEnv = append(result.SessionEnv, apitypes.PluginCLIBackendSessionEnvSummary{EnvVar: env.EnvVar, Source: env.Source, Required: env.Required})
		}
		seenEnv := make(map[string]struct{}, len(result.SessionEnv))
		for _, env := range result.SessionEnv {
			seenEnv[env.EnvVar] = struct{}{}
		}
		for _, requirement := range pkg.Extension.OAuth {
			for _, binding := range requirement.Bindings {
				if binding.EnvVar == "" {
					continue
				}
				if _, ok := seenEnv[binding.EnvVar]; ok {
					continue
				}
				seenEnv[binding.EnvVar] = struct{}{}
				result.SessionEnv = append(result.SessionEnv, apitypes.PluginCLIBackendSessionEnvSummary{EnvVar: binding.EnvVar, Source: "oauth." + binding.Credential, Required: true})
			}
		}
	}
	for _, skill := range pkg.Skills {
		result.Skills = append(result.Skills, apitypes.PluginCLIBackendSkillSummary{Name: skill.Name})
	}
	for _, server := range pkg.MCPServers {
		transport := apitypes.PluginMCPServerSummaryTransport(strings.ReplaceAll(server.Type, "-", "_"))
		if !transport.Valid() {
			transport = apitypes.StreamableHttp
		}
		declaration, configured := resource.MCP[server.Name]
		result.McpServers = append(result.McpServers, apitypes.PluginMCPServerSummary{
			ServerKey:                   server.Name,
			Transport:                   &transport,
			EndpointConfigured:          configured && declaration.URL != "",
			BearerConfigured:            false,
			CredentialMode:              nil,
			AuthType:                    nil,
			OauthClientIdConfigured:     false,
			OauthClientSecretConfigured: false,
		})
	}
	return result
}
