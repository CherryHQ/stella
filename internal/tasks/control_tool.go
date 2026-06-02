package tasks

import (
	"encoding/json"
	"fmt"
	"maps"
)

// normalizePatch accepts a Go map, a struct, or raw JSON bytes and returns a
// map[string]any.
func normalizePatch(patch any) (map[string]any, error) {
	if patch == nil {
		return map[string]any{}, nil
	}
	switch p := patch.(type) {
	case map[string]any:
		return p, nil
	case []byte:
		var m map[string]any
		if err := json.Unmarshal(p, &m); err != nil {
			return nil, fmt.Errorf("task_control: patch is not valid JSON: %w", err)
		}
		return m, nil
	case string:
		if p == "" {
			return map[string]any{}, nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(p), &m); err != nil {
			return nil, fmt.Errorf("task_control: patch is not valid JSON: %w", err)
		}
		return m, nil
	default:
		// Round-trip through JSON for arbitrary structs.
		b, err := json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("task_control: patch not serialisable: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("task_control: patch not a JSON object: %w", err)
		}
		return m, nil
	}
}

// mergeContext performs a shallow merge of patch into the current context
// JSON. Per D14 / HP6: existing top-level keys not present in patch are
// preserved. If existing is unparseable, patch wins entirely.
func mergeContext(existing string, patch map[string]any) string {
	var doc map[string]any
	if existing != "" && existing != "{}" {
		_ = json.Unmarshal([]byte(existing), &doc)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	maps.Copy(doc, patch)
	b, err := json.Marshal(doc)
	if err != nil {
		return existing
	}
	return string(b)
}
