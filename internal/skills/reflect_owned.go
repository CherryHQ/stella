package skills

import (
	"encoding/json"
	"fmt"
)

const (
	// ReflectSkillCreatedBy marks skills whose lifecycle is owned by Reflect.
	ReflectSkillCreatedBy = "reflect"
	// ManualSkillCreatedBy marks skills written through a user-originated path.
	ManualSkillCreatedBy = "manual"

	reflectSkillCreatedByKey = "created_by"
)

// MarkReflectOwnedMetadata records Reflect ownership without dropping existing
// install/tool metadata such as created-at, source, or version.
func MarkReflectOwnedMetadata(metadata json.RawMessage) (json.RawMessage, error) {
	return markSkillCreatedByMetadata(metadata, ReflectSkillCreatedBy)
}

// MarkManualOwnedMetadata records manual ownership without dropping installer
// metadata. Generic PGStore.Create always uses this boundary helper.
func MarkManualOwnedMetadata(metadata json.RawMessage) (json.RawMessage, error) {
	return markSkillCreatedByMetadata(metadata, ManualSkillCreatedBy)
}

// CreatedBy returns durable lifecycle ownership. Invalid or absent metadata has
// no owner, which keeps legacy rows out of Reflect-only paths.
func CreatedBy(sk Skill) string {
	fields, ok := skillMetadataFields(sk.Metadata)
	if !ok {
		return ""
	}
	createdBy, _ := fields[reflectSkillCreatedByKey].(string)
	return createdBy
}

func markSkillCreatedByMetadata(metadata json.RawMessage, createdBy string) (json.RawMessage, error) {
	fields := map[string]any{}
	if len(metadata) > 0 && string(metadata) != "null" {
		if err := json.Unmarshal(metadata, &fields); err != nil {
			return nil, fmt.Errorf("skills: decode metadata: %w", err)
		}
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields[reflectSkillCreatedByKey] = createdBy

	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("skills: encode metadata: %w", err)
	}
	return json.RawMessage(out), nil
}

// IsReflectOwned reports whether a skill is safe for Reflect-managed create,
// patch, or lifecycle flows. Malformed metadata is treated as not owned.
func IsReflectOwned(sk Skill) bool {
	return CreatedBy(sk) == ReflectSkillCreatedBy
}

func skillMetadataFields(metadata json.RawMessage) (map[string]any, bool) {
	if len(metadata) == 0 {
		return nil, false
	}
	fields := map[string]any{}
	if err := json.Unmarshal(metadata, &fields); err != nil {
		return nil, false
	}
	return fields, true
}
