package reflect

import (
	"fmt"
	"time"
)

const (
	PluginID            = "reflect"
	RuntimeName         = "service"
	defaultReviewBatch  = 5
	defaultReviewPeriod = time.Hour
)

type PluginConfig struct {
	Interval time.Duration
	Batch    int
}

func DefaultPluginConfig() map[string]any {
	return map[string]any{}
}

func PluginConfigSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"interval": map[string]any{
				"type":        "string",
				"description": "Review interval as a Go duration string.",
				"default":     defaultReviewPeriod.String(),
			},
			"batch": map[string]any{
				"type":        "integer",
				"description": "Maximum number of conversations reviewed per run.",
				"minimum":     1,
				"default":     defaultReviewBatch,
			},
		},
	}
}

func DecodePluginConfig(raw map[string]any) (PluginConfig, error) {
	cfg := PluginConfig{
		Interval: defaultReviewPeriod,
		Batch:    defaultReviewBatch,
	}
	if raw == nil {
		return cfg, nil
	}
	if v, ok := raw["interval"]; ok {
		interval, err := decodeDuration(v)
		if err != nil {
			return PluginConfig{}, fmt.Errorf("interval: %w", err)
		}
		if interval <= 0 {
			return PluginConfig{}, fmt.Errorf("interval: must be greater than 0")
		}
		cfg.Interval = interval
	}
	if v, ok := raw["batch"]; ok {
		batch, err := decodeInt(v)
		if err != nil {
			return PluginConfig{}, fmt.Errorf("batch: %w", err)
		}
		if batch <= 0 {
			return PluginConfig{}, fmt.Errorf("batch: must be greater than 0")
		}
		cfg.Batch = batch
	}
	return cfg, nil
}

func RedactPluginConfig(raw map[string]any) map[string]any {
	return cloneMap(raw)
}

func decodeDuration(value any) (time.Duration, error) {
	text, ok := value.(string)
	if !ok {
		return 0, fmt.Errorf("must be a duration string")
	}
	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func decodeInt(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("must be an integer")
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("must be an integer")
	}
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
