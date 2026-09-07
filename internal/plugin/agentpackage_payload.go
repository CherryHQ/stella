package plugin

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

// ResourcePayloadFromAgentPackage converts the portable package declaration to
// the one persisted Plugin resource shape. Keeping this conversion here makes
// generated builtin definitions and future imports agree on ownership and
// preserves the manifest version as part of the published Spec.
func ResourcePayloadFromAgentPackage(pkg *agentpackage.Package) (ResourcePayload, error) {
	if pkg == nil {
		return ResourcePayload{}, fmt.Errorf("plugin: nil Agent package")
	}
	payload := ResourcePayload{Version: pkg.Manifest.Version, Description: pkg.Manifest.Description, Origin: "package"}
	if extension := pkg.Extension; extension != nil {
		payload.Prompt = extension.Prompt
		for _, binary := range extension.Binaries {
			options, err := decodePackageOptions(binary.Options)
			if err != nil {
				return ResourcePayload{}, fmt.Errorf("binary %q options: %w", binary.Name, err)
			}
			payload.Binaries = append(payload.Binaries, BinaryResource{
				Name: binary.Name, Tool: binary.Tool, Version: binary.Version,
				Options: options,
			})
		}
		for _, env := range extension.SessionEnv {
			payload.SessionEnvs = append(payload.SessionEnvs, SessionEnvResource{
				EnvVar: env.EnvVar, Source: env.Source, Required: env.Required,
			})
		}
		for _, requirement := range extension.OAuth {
			converted := OAuthRequirement{Provider: requirement.Provider, Scopes: slices.Clone(requirement.Scopes)}
			for _, binding := range requirement.Bindings {
				converted.Bindings = append(converted.Bindings, OAuthBinding{
					Credential: binding.Credential, EnvVar: binding.EnvVar, Connection: binding.Connection,
				})
			}
			payload.OAuth = append(payload.OAuth, converted)
		}
	}
	for _, server := range pkg.MCPServers {
		transport, ok := agentMCPTransport(server.Type)
		if !ok {
			return ResourcePayload{}, fmt.Errorf("package %q MCP server %q uses unsupported transport %q", pkg.Manifest.Name, server.Name, server.Type)
		}
		if payload.MCPServers == nil {
			payload.MCPServers = make(map[string]MCPServerResource)
		}
		payload.MCPServers[server.Name] = MCPServerResource{
			URL: server.URL, Transport: transport, AuthType: "none", CredentialMode: "shared",
			Headers: maps.Clone(server.Headers),
		}
	}
	for _, skill := range pkg.Skills {
		payload.Skills = append(payload.Skills, SkillResource{Name: skill.Name})
	}
	return payload, nil
}

func agentMCPTransport(transport string) (string, bool) {
	switch transport {
	case "streamable-http":
		return "streamable_http", true
	case "sse":
		return "sse", true
	default:
		return "", false
	}
}

func decodePackageOptions(in map[string]json.RawMessage) (map[string]any, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(in))
	for key, raw := range in {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out[key] = value
	}
	return out, nil
}
