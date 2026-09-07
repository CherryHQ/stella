package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/mcp"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
)

// pluginResourceSummary is the only response projection of plugin.Config.Payload.
// Each constructor below decodes into a backend-owned typed shape and copies
// only fields explicitly approved for the public management response.
func pluginResourceSummary(definition pluginpkg.Definition, config pluginpkg.Config) (apitypes.PluginResourceSummary, error) {
	result := apitypes.PluginResourceSummary{
		Binaries:   []apitypes.PluginCLIBackendBinarySummary{},
		Skills:     []apitypes.PluginCLIBackendSkillSummary{},
		SessionEnv: []apitypes.PluginCLIBackendSessionEnvSummary{},
		McpServers: []apitypes.PluginMCPServerSummary{},
	}
	if config.Enabled != nil && !*config.Enabled && emptyJSON(config.Payload) {
		return result, nil
	}
	cli, err := cliBackendSummary(definition.Spec, config.Payload, config.Enabled)
	if err != nil {
		return result, err
	}
	result.Binaries, result.Skills, result.SessionEnv = cli.Binaries, cli.Skills, cli.SessionEnv
	result.OauthProviderConfigured = cli.OauthProviderConfigured
	if hasMCPResources(definition.Spec, config.Payload) {
		mcpSummary, err := mcpResourceSummaries(definition.Spec, config.Payload, config.CredentialRefs, config.MCPServers, config.Revision)
		if err != nil {
			return result, err
		}
		result.McpServers = mcpSummary
	}
	return result, nil
}

func hasMCPResources(definitionSpec, configPayload json.RawMessage) bool {
	for _, raw := range []json.RawMessage{definitionSpec, configPayload} {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) == nil {
			if _, ok := object["mcp_servers"]; ok {
				return true
			}
			// A single MCP resource is accepted in the compact authoring form
			// used by marketplace installs. The backend expands it into the
			// composable `mcp_servers.main` child during creation.
			for _, key := range []string{"url", "transport", "auth_type", "credential_mode"} {
				if _, ok := object[key]; ok {
					return true
				}
			}
		}
	}
	return false
}

type cliResourceSummary struct {
	Binaries                []apitypes.PluginCLIBackendBinarySummary
	Skills                  []apitypes.PluginCLIBackendSkillSummary
	SessionEnv              []apitypes.PluginCLIBackendSessionEnvSummary
	OauthProviderConfigured bool
}

func cliBackendSummary(definitionSpec, configPayload json.RawMessage, enabled *bool) (cliResourceSummary, error) {
	result := cliResourceSummary{
		Binaries:                []apitypes.PluginCLIBackendBinarySummary{},
		Skills:                  []apitypes.PluginCLIBackendSkillSummary{},
		SessionEnv:              []apitypes.PluginCLIBackendSessionEnvSummary{},
		OauthProviderConfigured: false,
	}
	// A negative config intentionally has no selected resource to project. The
	// builtin system row uses enabled=NULL and an empty overlay, so it still
	// projects its shipped resources below.
	if enabled != nil && !*enabled && emptyJSON(configPayload) {
		return result, nil
	}
	raw, err := mergeCLISummaryPayload(definitionSpec, configPayload)
	if err != nil {
		return result, err
	}
	// A package can carry MCP and CLI resources together. Project only the
	// allowlisted CLI keys so MCP endpoint/auth fields and package metadata do
	// not make the safe summary decoder reject the whole package.
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return result, fmt.Errorf("resolved CLI payload: invalid object")
	}
	allowed := map[string]struct{}{"description": {}, "category": {}, "prompt": {}, "binaries": {}, "skills": {}, "session_env": {}, "oauth_provider": {}, "oauth": {}}
	filtered := make(map[string]json.RawMessage)
	for key, value := range object {
		if _, ok := allowed[key]; ok {
			filtered[key] = value
		}
	}
	filteredRaw, err := json.Marshal(filtered)
	if err != nil {
		return result, fmt.Errorf("resolved CLI payload: %w", err)
	}
	payload, err := manifest.DecodeCLIPayload(filteredRaw, "resolved CLI payload")
	if err != nil {
		return result, err
	}
	for _, binary := range payload.Binaries {
		result.Binaries = append(result.Binaries, apitypes.PluginCLIBackendBinarySummary{
			Name: binary.Name, Version: binary.Version,
		})
	}
	for _, skill := range payload.Skills {
		result.Skills = append(result.Skills, apitypes.PluginCLIBackendSkillSummary{
			Name: skill.Name,
		})
	}
	for _, env := range payload.SessionEnvs {
		result.SessionEnv = append(result.SessionEnv, apitypes.PluginCLIBackendSessionEnvSummary{
			EnvVar: env.EnvVar, Source: env.Source, Required: env.Required,
		})
	}
	envVars := make(map[string]struct{}, len(result.SessionEnv))
	for _, env := range result.SessionEnv {
		envVars[env.EnvVar] = struct{}{}
	}
	for _, requirement := range payload.OAuth {
		for _, binding := range requirement.Bindings {
			if strings.TrimSpace(binding.EnvVar) == "" {
				continue
			}
			if _, exists := envVars[binding.EnvVar]; exists {
				continue
			}
			envVars[binding.EnvVar] = struct{}{}
			result.SessionEnv = append(result.SessionEnv, apitypes.PluginCLIBackendSessionEnvSummary{
				EnvVar: binding.EnvVar, Source: "oauth." + binding.Credential, Required: true,
			})
		}
	}
	result.OauthProviderConfigured = strings.TrimSpace(payload.OAuthProvider) != "" || len(payload.OAuth) > 0
	return result, nil
}

func mergeCLISummaryPayload(definitionSpec, configPayload json.RawMessage) (json.RawMessage, error) {
	base := map[string]json.RawMessage{}
	if !emptyJSON(definitionSpec) {
		if err := json.Unmarshal(definitionSpec, &base); err != nil || base == nil {
			return nil, fmt.Errorf("invalid CLI definition payload")
		}
	}
	if !emptyJSON(configPayload) {
		var overlay map[string]json.RawMessage
		if err := json.Unmarshal(configPayload, &overlay); err != nil || overlay == nil {
			return nil, fmt.Errorf("invalid CLI config payload")
		}
		maps.Copy(base, overlay)
	}
	return json.Marshal(base)
}

type mcpSummaryPayload struct {
	URL            *string             `json:"url,omitempty"`
	Transport      *string             `json:"transport,omitempty"`
	AuthType       *string             `json:"auth_type,omitempty"`
	CredentialMode *string             `json:"credential_mode,omitempty"`
	Metadata       *mcpSummaryMetadata `json:"metadata,omitempty"`
}

type mcpSummaryMetadata struct {
	OAuth *mcpOAuthSummaryMetadata `json:"oauth,omitempty"`
}

type mcpOAuthSummaryMetadata struct {
	ClientID string `json:"client_id,omitempty"`
}

type mcpCredentialSummaryRefs struct {
	Bearer            json.RawMessage `json:"bearer,omitempty"`
	OAuthBundle       json.RawMessage `json:"oauth_bundle,omitempty"`
	OAuthClientSecret json.RawMessage `json:"oauth_client_secret,omitempty"`
}

func mcpResourceSummaries(definitionSpec, configPayload, credentialRefs json.RawMessage, children []pluginpkg.MCPServerChild, revision int64) ([]apitypes.PluginMCPServerSummary, error) {
	merged, err := mergeMCPBackendSummaryObjects(definitionSpec, configPayload)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(merged, &object); err != nil || object == nil {
		return nil, fmt.Errorf("invalid MCP backend payload")
	}
	childByKey := make(map[string]pluginpkg.MCPServerChild, len(children))
	for _, child := range children {
		childByKey[child.ServerKey] = child
	}
	if raw, ok := object["mcp_servers"]; ok {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(raw, &servers); err != nil || servers == nil {
			return nil, fmt.Errorf("invalid MCP server map")
		}
		out := make([]apitypes.PluginMCPServerSummary, 0, len(servers))
		keys := make([]string, 0, len(servers))
		for key := range servers {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			refs, err := decodeMCPCredentialSummaryRefs(credentialRefs, key)
			if err != nil {
				return nil, err
			}
			value, err := mcpResourceSummaryForPayload(key, servers[key], refs, childByKey[key], revision)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	}
	refs, err := decodeMCPCredentialSummaryRefs(credentialRefs, "main")
	if err != nil {
		return nil, err
	}
	value, err := mcpResourceSummaryForPayload("main", merged, refs, childByKey["main"], revision)
	if err != nil {
		return nil, err
	}
	return []apitypes.PluginMCPServerSummary{value}, nil
}

func mergeMCPBackendSummaryObjects(definitionSpec, configPayload json.RawMessage) (json.RawMessage, error) {
	base := map[string]json.RawMessage{}
	if !emptyJSON(definitionSpec) {
		if err := json.Unmarshal(definitionSpec, &base); err != nil || base == nil {
			return nil, fmt.Errorf("invalid MCP definition payload")
		}
	}
	if !emptyJSON(configPayload) {
		var overlay map[string]json.RawMessage
		if err := json.Unmarshal(configPayload, &overlay); err != nil || overlay == nil {
			return nil, fmt.Errorf("invalid MCP config payload")
		}
		maps.Copy(base, overlay)
	}
	return json.Marshal(base)
}

func mcpResourceSummaryForPayload(key string, raw json.RawMessage, refs mcpCredentialSummaryRefs, child pluginpkg.MCPServerChild, revision int64) (apitypes.PluginMCPServerSummary, error) {
	payload, err := decodeMCPBackendSummaryPayload(raw)
	if err != nil {
		return apitypes.PluginMCPServerSummary{}, err
	}
	transport := mcp.TransportStreamableHTTP
	if payload.Transport != nil && *payload.Transport != "" {
		transport = *payload.Transport
	}
	if !mcp.ValidTransport(transport) {
		return apitypes.PluginMCPServerSummary{}, fmt.Errorf("unsupported MCP transport %q", transport)
	}
	authType := mcp.AuthTypeNone
	if payload.AuthType != nil && *payload.AuthType != "" {
		authType = *payload.AuthType
	}
	if !mcp.ValidAuthType(authType) {
		return apitypes.PluginMCPServerSummary{}, fmt.Errorf("unsupported MCP auth type %q", authType)
	}
	credentialMode := mcp.CredentialModeShared
	if payload.CredentialMode != nil && *payload.CredentialMode != "" {
		credentialMode = *payload.CredentialMode
	}
	if !mcp.ValidCredentialMode(credentialMode) {
		return apitypes.PluginMCPServerSummary{}, fmt.Errorf("unsupported MCP credential mode %q", credentialMode)
	}
	var childID *uuid.UUID
	var parentID *uuid.UUID
	var parentRevision *int64
	if child.ID != "" {
		if id, e := uuid.Parse(child.ID); e == nil {
			childID = &id
		}
		if id, e := uuid.Parse(child.ParentConfigID); e == nil {
			parentID = &id
		}
	}
	if child.ID != "" {
		rev := revision
		parentRevision = &rev
	}
	return apitypes.PluginMCPServerSummary{
		ServerKey: key, ChildId: childID, ParentConfigId: parentID, ParentRevision: parentRevision,
		Transport:                   apiPtr(apitypes.PluginMCPServerSummaryTransport(transport)),
		AuthType:                    apiPtr(apitypes.PluginMCPServerSummaryAuthType(authType)),
		CredentialMode:              apiPtr(apitypes.PluginMCPServerSummaryCredentialMode(credentialMode)),
		EndpointConfigured:          payload.URL != nil && strings.TrimSpace(*payload.URL) != "",
		BearerConfigured:            configuredJSON(refs.Bearer),
		OauthClientIdConfigured:     payload.Metadata != nil && payload.Metadata.OAuth != nil && strings.TrimSpace(payload.Metadata.OAuth.ClientID) != "",
		OauthClientSecretConfigured: configuredJSON(refs.OAuthClientSecret),
	}, nil
}

func apiPtr[T any](v T) *T { return &v }

func decodeMCPBackendSummaryPayload(raw json.RawMessage) (mcpSummaryPayload, error) {
	if emptyJSON(raw) {
		return mcpSummaryPayload{}, nil
	}
	var payload mcpSummaryPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return mcpSummaryPayload{}, fmt.Errorf("invalid MCP backend payload: %w", err)
	}
	return payload, nil
}

func decodeMCPCredentialSummaryRefs(raw json.RawMessage, key string) (mcpCredentialSummaryRefs, error) {
	if emptyJSON(raw) {
		return mcpCredentialSummaryRefs{}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return mcpCredentialSummaryRefs{}, fmt.Errorf("invalid MCP credential refs: %w", err)
	}
	if nested, ok := object["mcp_servers"]; ok {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(nested, &servers); err != nil || servers == nil {
			return mcpCredentialSummaryRefs{}, fmt.Errorf("invalid MCP credential refs: mcp_servers must be an object")
		}
		child, ok := servers[key]
		if !ok {
			return mcpCredentialSummaryRefs{}, nil
		}
		var refs mcpCredentialSummaryRefs
		if err := json.Unmarshal(child, &refs); err != nil {
			return refs, fmt.Errorf("invalid MCP credential refs for %q: %w", key, err)
		}
		return refs, nil
	}
	var refs mcpCredentialSummaryRefs
	if err := json.Unmarshal(raw, &refs); err != nil {
		return refs, fmt.Errorf("invalid MCP credential refs: %w", err)
	}
	return refs, nil
}

func emptyJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}"))
}

func configuredJSON(raw json.RawMessage) bool {
	return !emptyJSON(raw)
}
