package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// This adapter is for migration and old HTTP input only, never execution.
func decodeParameterObject(raw json.RawMessage, label string) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, fmt.Errorf("%w: %s must be an object", ErrInvalidConfig, label)
	}
	return value, nil
}

// prepareCustomDefinitionSpec is kept at the legacy import/HTTP boundary. It
// turns the old flat remote-MCP form into an authored declaration before the
// definition is published; persisted config updates never call it.
func prepareCustomDefinitionSpec(spec, config json.RawMessage) (json.RawMessage, error) {
	definition, err := decodeParameterObject(spec, "definition spec")
	if err != nil {
		return nil, err
	}
	if len(definition) != 0 {
		return spec, nil
	}
	configFields, err := decodeParameterObject(config, "config payload")
	if err != nil {
		return nil, err
	}
	if raw, ok := configFields["mcp_servers"]; ok {
		servers, err := decodeParameterObject(raw, "MCP config")
		if err != nil {
			return nil, err
		}
		keys := make(map[string]json.RawMessage, len(servers))
		for key, rawServer := range servers {
			server, err := decodeParameterObject(rawServer, "MCP server "+key)
			if err != nil {
				return nil, err
			}
			declaration := make(map[string]json.RawMessage, len(server))
			for _, field := range []string{"description", "url", "transport", "auth_type", "credential_mode", "headers", "metadata"} {
				if value, ok := server[field]; ok {
					declaration[field] = value
				}
			}
			keys[key] = mustMarshalRaw(declaration)
		}
		return json.Marshal(map[string]json.RawMessage{
			"origin":      json.RawMessage(`"remote_mcp"`),
			"mcp_servers": mustMarshalRaw(keys),
		})
	}
	flat := make(map[string]json.RawMessage)
	hasConnection := false
	for _, field := range []string{"url", "transport", "auth_type", "credential_mode", "headers", "metadata"} {
		if value, ok := configFields[field]; ok {
			flat[field] = value
			hasConnection = true
		}
	}
	if !hasConnection {
		return spec, nil
	}
	if value, ok := configFields["description"]; ok {
		flat["description"] = value
	}
	return json.Marshal(map[string]json.RawMessage{
		"origin":      json.RawMessage(`"remote_mcp"`),
		"mcp_servers": mustMarshalRaw(map[string]json.RawMessage{"main": mustMarshalRaw(flat)}),
	})
}

// promoteLegacyMCPDefinition converts a manifest's old flat endpoint fields
// into the formal declaration while preserving unrelated authored fields.
func promoteLegacyMCPDefinition(raw json.RawMessage) (json.RawMessage, error) {
	object, err := decodeParameterObject(raw, "definition spec")
	if err != nil {
		return nil, err
	}
	if _, ok := object["mcp_servers"]; ok {
		return raw, nil
	}
	flat := make(map[string]json.RawMessage)
	hasConnection := false
	for _, field := range []string{"url", "transport", "auth_type", "credential_mode", "headers", "metadata"} {
		if value, ok := object[field]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			flat[field] = value
			hasConnection = true
			delete(object, field)
		}
	}
	if !hasConnection {
		return raw, nil
	}
	if value, ok := object["description"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		flat["description"] = value
		delete(object, "description")
	}
	if _, ok := object["origin"]; !ok {
		object["origin"] = json.RawMessage(`"remote_mcp"`)
	}
	object["mcp_servers"] = mustMarshalRaw(map[string]json.RawMessage{"main": mustMarshalRaw(flat)})
	return json.Marshal(object)
}

// NormalizeHTTPDefinitionSpec adapts the legacy HTTP create shape before it
// reaches Access. Persisted definitions and runtime callers must use the
// formal resource declaration directly.
func NormalizeHTTPDefinitionSpec(spec, config json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(config)) == 0 || bytes.Equal(bytes.TrimSpace(config), []byte("null")) {
		return spec, nil
	}
	return prepareCustomDefinitionSpec(spec, config)
}

func mustMarshalRaw(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func legacyConfigParameters(raw json.RawMessage, base map[string]json.RawMessage) (json.RawMessage, error) {
	fields, err := decodeParameterObject(raw, "config payload")
	if err != nil {
		return nil, err
	}
	if _, ok := fields["binaries"]; ok {
		var array []map[string]json.RawMessage
		if err := json.Unmarshal(fields["binaries"], &array); err == nil && array != nil {
			declared, err := declaredBinaryTools(base["binaries"])
			if err != nil {
				return nil, err
			}
			mapped := make(map[string]json.RawMessage, len(array))
			for index, item := range array {
				name, ok := item["name"]
				var nameValue string
				if !ok || json.Unmarshal(name, &nameValue) != nil || nameValue == "" {
					return nil, fmt.Errorf("%w: binary[%d] requires name", ErrInvalidConfig, index)
				}
				if _, exists := mapped[nameValue]; exists {
					return nil, fmt.Errorf("%w: duplicate binary %q", ErrInvalidConfig, nameValue)
				}
				tool, ok := declared[nameValue]
				if !ok {
					return nil, fmt.Errorf("%w: binary %q is not declared", ErrInvalidConfig, nameValue)
				}
				if rawTool, exists := item["tool"]; exists {
					var supplied string
					if json.Unmarshal(rawTool, &supplied) != nil || supplied != tool {
						return nil, fmt.Errorf("%w: binary %q changes its declared tool", ErrInvalidConfig, nameValue)
					}
				}
				parameters := make(map[string]json.RawMessage, 2)
				for field, value := range item {
					if field == "version" || field == "options" {
						parameters[field] = value
						continue
					}
					if field != "name" && field != "tool" {
						return nil, fmt.Errorf("%w: binary %q contains unsupported field %q", ErrInvalidConfig, nameValue, field)
					}
				}
				encoded, err := json.Marshal(parameters)
				if err != nil {
					return nil, err
				}
				mapped[nameValue] = encoded
			}
			encoded, err := json.Marshal(mapped)
			if err != nil {
				return nil, err
			}
			fields["binaries"] = encoded
		}
	}
	flat := map[string]json.RawMessage{}
	hasConnection := false
	for _, field := range []string{"url", "transport", "auth_type", "credential_mode", "headers", "metadata"} {
		if value, ok := fields[field]; ok {
			flat[field] = value
			hasConnection = true
			delete(fields, field)
		}
	}
	if hasConnection {
		if value, ok := fields["description"]; ok {
			flat["description"] = value
			delete(fields, "description")
		}
		var servers map[string]json.RawMessage
		if rawServers := base["mcp_servers"]; len(rawServers) != 0 {
			_ = json.Unmarshal(rawServers, &servers)
		}
		if len(servers) != 1 {
			return nil, fmt.Errorf("%w: flat MCP config requires exactly one declared server", ErrInvalidConfig)
		}
		for name := range servers {
			encoded, err := json.Marshal(flat)
			if err != nil {
				return nil, err
			}
			fields["mcp_servers"], err = json.Marshal(map[string]json.RawMessage{name: encoded})
			if err != nil {
				return nil, err
			}
			break
		}
	}
	return json.Marshal(fields)
}

func declaredBinaryTools(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: binary config has no declared binaries", ErrInvalidConfig)
	}
	var declarations []struct {
		Name string `json:"name"`
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal(raw, &declarations); err != nil {
		return nil, fmt.Errorf("%w: invalid binary declarations: %w", ErrInvalidConfig, err)
	}
	result := make(map[string]string, len(declarations))
	for _, declaration := range declarations {
		if declaration.Name == "" || declaration.Tool == "" {
			return nil, fmt.Errorf("%w: binary declaration requires name and tool", ErrInvalidConfig)
		}
		if _, exists := result[declaration.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate declared binary %q", ErrInvalidConfig, declaration.Name)
		}
		result[declaration.Name] = declaration.Tool
	}
	return result, nil
}

// NormalizeHTTPConfigParameters adapts old HTTP payloads (binary arrays and
// compact MCP fields) into named ConfigParameters. This is deliberately an
// edge adapter; service and runtime paths accept only the formal shape.
func NormalizeHTTPConfigParameters(spec, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || emptyJSONObject(raw) {
		if len(raw) == 0 {
			return nil, nil
		}
		return json.RawMessage(`{}`), nil
	}
	base, err := decodeParameterObject(spec, "definition spec")
	if err != nil {
		return nil, err
	}
	normalized, err := legacyConfigParameters(raw, base)
	if err != nil {
		return nil, err
	}
	if _, err := DecodeConfigParameters(normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}
