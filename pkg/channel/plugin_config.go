package channel

import (
	"encoding/json"
	"fmt"
)

func DecodePluginConfig[T any](raw map[string]any, label string) (T, error) {
	var cfg T
	if raw == nil {
		return cfg, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("marshal %s config: %w", label, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("decode %s config: %w", label, err)
	}
	return cfg, nil
}

func CloneConfigMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
