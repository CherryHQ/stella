package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/CherryHQ/stella/resources"
)

type builtinDefinition struct {
	ID             string          `json:"id"`
	DisplayName    string          `json:"display_name"`
	DefaultEnabled bool            `json:"default_enabled"`
	Revision       int64           `json:"revision"`
	Spec           json.RawMessage `json:"spec"`
}

// BuiltinDefinitions returns the immutable release catalog generated from
// standard Agent packages. The embedded catalog contains only plugin identity
// and resource declarations; database-owned fields are created during sync.
func BuiltinDefinitions() ([]Definition, error) {
	decoder := json.NewDecoder(bytes.NewReader(resources.BuiltinPluginsJSON()))
	decoder.DisallowUnknownFields()
	var entries []builtinDefinition
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode builtin plugin catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode builtin plugin catalog: trailing JSON")
		}
		return nil, fmt.Errorf("decode builtin plugin catalog: %w", err)
	}
	if entries == nil {
		return nil, fmt.Errorf("decode builtin plugin catalog: expected array")
	}
	definitions := make([]Definition, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, exists := seen[entry.ID]; exists {
			return nil, fmt.Errorf("builtin plugin catalog contains duplicate ID %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		definition := Definition{
			ID: entry.ID, DisplayName: entry.DisplayName, Source: SourceBuiltin,
			Spec: entry.Spec, DefaultEnabled: entry.DefaultEnabled, Revision: entry.Revision,
		}
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("builtin plugin %q: %w", entry.ID, err)
		}
		payload, err := DecodeResourcePayload(entry.Spec, "builtin plugin "+entry.ID)
		if err != nil {
			return nil, err
		}
		if err := ValidateResourceDeclarations(payload, "builtin plugin "+entry.ID, nil); err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}
