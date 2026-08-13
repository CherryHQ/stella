package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrSkillNotMutable rejects immutable, deprecated, and project writes.
var ErrSkillNotMutable = errors.New("skill is not mutable")

// ErrInvalidSkillFilePath rejects keys whose runtime path would differ from the
// canonical relative path committed to Home.
var ErrInvalidSkillFilePath = errors.New("invalid skill file path")

func validateSkillFilePaths(files map[string]string) error {
	for raw := range files {
		clean := path.Clean(raw)
		if raw == "" || strings.ContainsRune(raw, '\x00') || strings.Contains(raw, "\\") || path.IsAbs(raw) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != raw {
			return fmt.Errorf("%w: %q must be a canonical relative path", ErrInvalidSkillFilePath, raw)
		}
	}
	return nil
}

func managedUpdateMetadata(metadata json.RawMessage, existingCreatedBy string, convertToManual bool) (json.RawMessage, error) {
	fields := map[string]any{}
	if len(metadata) > 0 && string(metadata) != "null" {
		if err := json.Unmarshal(metadata, &fields); err != nil {
			return nil, fmt.Errorf("decode managed skill metadata: %w", err)
		}
	}
	if fields == nil {
		fields = map[string]any{}
	}
	switch {
	case convertToManual:
		fields[reflectSkillCreatedByKey] = ManualSkillCreatedBy
	case existingCreatedBy == "":
		delete(fields, reflectSkillCreatedByKey)
	default:
		fields[reflectSkillCreatedByKey] = existingCreatedBy
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode managed skill metadata: %w", err)
	}
	return json.RawMessage(out), nil
}
