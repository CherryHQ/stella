package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// ConfigParameters contains only parameters of already-declared resources.
// Ownership, executable sources, skills and OAuth requirements belong to Spec.
type ConfigParameters struct {
	Binaries   map[string]BinaryParameters `json:"binaries,omitempty"`
	MCPServers map[string]MCPParameters    `json:"mcp_servers,omitzero"`
}

type BinaryParameters struct {
	Version string         `json:"version,omitempty"`
	Options map[string]any `json:"options,omitempty"`
}

type MCPParameters struct {
	Description    *string           `json:"description,omitempty"`
	URL            string            `json:"url,omitempty"`
	Transport      string            `json:"transport,omitempty"`
	AuthType       string            `json:"auth_type,omitempty"`
	CredentialMode string            `json:"credential_mode,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
}

func DecodeConfigParameters(raw json.RawMessage) (ConfigParameters, error) {
	var parameters ConfigParameters
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&parameters); err != nil {
		return ConfigParameters{}, fmt.Errorf("%w: parameters: %w", ErrInvalidConfig, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ConfigParameters{}, fmt.Errorf("%w: parameters must be one object", ErrInvalidConfig)
	}
	// Go decodes null into zero values. Reject it at the parameter boundary;
	// clearing a parameter uses the explicit reset operation instead.
	var shape map[string]map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil || shape == nil {
		return ConfigParameters{}, fmt.Errorf("%w: parameters must be an object", ErrInvalidConfig)
	}
	for resource, entries := range shape {
		if entries == nil {
			return ConfigParameters{}, fmt.Errorf("%w: %s cannot be null", ErrInvalidConfig, resource)
		}
		for name, fields := range entries {
			if strings.TrimSpace(name) == "" || fields == nil {
				return ConfigParameters{}, fmt.Errorf("%w: %s requires a resource name and object", ErrInvalidConfig, resource)
			}
			for field, value := range fields {
				if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return ConfigParameters{}, fmt.Errorf("%w: %s.%s.%s cannot be null", ErrInvalidConfig, resource, name, field)
				}
			}
		}
	}
	return parameters, nil
}

// applyConfigParameters never mutates the shared declaration. Packages retain
// their fixed resource set; remote-MCP configs select their own declared keys.
// Backend validation checks parameter safety and completeness before use.
func applyConfigParameters(declaration ResourcePayload, parameters ConfigParameters) (ResourcePayload, error) {
	declaration.Binaries = slices.Clone(declaration.Binaries)
	declaration.MCPServers = maps.Clone(declaration.MCPServers)
	byName := make(map[string]int, len(declaration.Binaries))
	for i, binary := range declaration.Binaries {
		if _, duplicate := byName[binary.Name]; duplicate || binary.Name == "" {
			return ResourcePayload{}, fmt.Errorf("%w: invalid or duplicate binary name", ErrInvalidDefinition)
		}
		byName[binary.Name] = i
	}
	for name, parameter := range parameters.Binaries {
		index, ok := byName[name]
		if !ok {
			return ResourcePayload{}, fmt.Errorf("%w: unknown binary %q", ErrInvalidConfig, name)
		}
		if parameter.Version != "" {
			declaration.Binaries[index].Version = parameter.Version
		}
		if parameter.Options != nil {
			declaration.Binaries[index].Options = maps.Clone(parameter.Options)
		}
	}
	for name, parameter := range parameters.MCPServers {
		server, ok := declaration.MCPServers[name]
		if !ok {
			return ResourcePayload{}, fmt.Errorf("%w: unknown MCP server %q", ErrInvalidConfig, name)
		}
		if parameter.Description != nil {
			server.Description = *parameter.Description
		}
		if parameter.URL != "" {
			server.URL = parameter.URL
		}
		if parameter.Transport != "" {
			server.Transport = parameter.Transport
		}
		if parameter.AuthType != "" {
			server.AuthType = parameter.AuthType
		}
		if parameter.CredentialMode != "" {
			server.CredentialMode = parameter.CredentialMode
		}
		if parameter.Headers != nil {
			server.Headers = maps.Clone(parameter.Headers)
		}
		if parameter.Metadata != nil {
			server.Metadata = maps.Clone(parameter.Metadata)
		}
		declaration.MCPServers[name] = server
	}
	// Personal remote-MCP configs own their child membership. A shared
	// declaration may contain keys belonging to other configs of this identity.
	if declaration.Origin == "remote_mcp" {
		for name := range declaration.MCPServers {
			if _, selected := parameters.MCPServers[name]; !selected {
				delete(declaration.MCPServers, name)
			}
		}
	}
	return declaration, nil
}

// MergeDefinitionConfig is the existing resolver seam. It accepts only formal
// parameters; legacy input conversion belongs to migration and HTTP adapters.
func MergeDefinitionConfig(base, overlay json.RawMessage) (json.RawMessage, error) {
	var declaration ResourcePayload
	if err := json.Unmarshal(base, &declaration); err != nil {
		return nil, fmt.Errorf("%w: spec: %w", ErrInvalidDefinition, err)
	}
	parameters, err := DecodeConfigParameters(overlay)
	if err != nil {
		return nil, err
	}
	effective, err := applyConfigParameters(declaration, parameters)
	if err != nil {
		return nil, err
	}
	return json.Marshal(effective)
}
