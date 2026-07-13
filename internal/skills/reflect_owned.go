package skills

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	// ReflectSkillCreatedBy marks skills whose lifecycle is owned by Reflect.
	ReflectSkillCreatedBy = "reflect"

	reflectSkillCreatedByKey = "created_by"
)

// MarkReflectOwnedMetadata records Reflect ownership without dropping existing
// install/tool metadata such as created-at, source, or version.
func MarkReflectOwnedMetadata(metadata json.RawMessage) (json.RawMessage, error) {
	fields := map[string]any{}
	if len(metadata) > 0 && string(metadata) != "null" {
		if !json.Valid(metadata) {
			var invalid any
			err := json.Unmarshal(metadata, &invalid)
			return nil, fmt.Errorf("skills: decode metadata: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(metadata))
		decoder.UseNumber()
		if err := decoder.Decode(&fields); err != nil {
			return nil, fmt.Errorf("skills: decode metadata: %w", err)
		}
	}
	// Decoding JSON null replaces the initialized map with nil.
	if fields == nil {
		fields = map[string]any{}
	}
	fields[reflectSkillCreatedByKey] = ReflectSkillCreatedBy

	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("skills: encode metadata: %w", err)
	}
	return json.RawMessage(out), nil
}

// IsReflectOwned reports whether a skill is safe for Reflect-managed create,
// patch, or lifecycle flows. Malformed metadata is treated as not owned.
func IsReflectOwned(sk Skill) bool {
	if len(sk.Metadata) == 0 {
		return false
	}
	fields := map[string]any{}
	if err := json.Unmarshal(sk.Metadata, &fields); err != nil {
		return false
	}
	createdBy, _ := fields[reflectSkillCreatedByKey].(string)
	return createdBy == ReflectSkillCreatedBy
}
