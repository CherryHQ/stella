package scheduler

import (
	"encoding/json"
	"fmt"
	"strings"
)

const pluginJobMessagePrefix = "__anna_plugin_job__:"

type pluginJobEnvelope struct {
	PluginID    string         `json:"plugin_id"`
	Key         string         `json:"key"`
	RuntimeName string         `json:"runtime_name"`
	Payload     map[string]any `json:"payload,omitempty"`
	Description string         `json:"description,omitempty"`
}

func EncodePluginJobMessage(pluginID, key, runtimeName, description string, payload map[string]any) (string, error) {
	body, err := json.Marshal(pluginJobEnvelope{
		PluginID:    pluginID,
		Key:         key,
		RuntimeName: runtimeName,
		Payload:     clonePayload(payload),
		Description: description,
	})
	if err != nil {
		return "", fmt.Errorf("encode plugin job: %w", err)
	}
	return pluginJobMessagePrefix + string(body), nil
}

func DecodePluginJob(job Job) (pluginID, key, runtimeName, description string, payload map[string]any, ok bool) {
	if !strings.HasPrefix(job.Message, pluginJobMessagePrefix) {
		return "", "", "", "", nil, false
	}
	var env pluginJobEnvelope
	if err := json.Unmarshal([]byte(strings.TrimPrefix(job.Message, pluginJobMessagePrefix)), &env); err != nil {
		return "", "", "", "", nil, false
	}
	return env.PluginID, env.Key, env.RuntimeName, env.Description, clonePayload(env.Payload), env.PluginID != "" && env.Key != ""
}

func IsPluginJob(job Job) bool {
	_, _, _, _, _, ok := DecodePluginJob(job)
	return ok
}

func clonePayload(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
