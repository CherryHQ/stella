package scheduler

import (
	"encoding/json"
	"strings"
)

const legacyPluginJobMessagePrefix = "__anna_plugin_job__:"

type legacyPluginJobEnvelope struct {
	PluginID    string         `json:"plugin_id"`
	Key         string         `json:"key"`
	RuntimeName string         `json:"runtime_name"`
	Payload     map[string]any `json:"payload,omitempty"`
	Description string         `json:"description,omitempty"`
}

// DecodePluginJob decodes the legacy reserved-message plugin job envelope.
// It remains only for migration of pre-schema-hardening scheduler rows.
func DecodePluginJob(job Job) (pluginID, key, runtimeName, description string, payload map[string]any, ok bool) {
	if !strings.HasPrefix(job.Message, legacyPluginJobMessagePrefix) {
		return "", "", "", "", nil, false
	}
	var env legacyPluginJobEnvelope
	if err := json.Unmarshal([]byte(strings.TrimPrefix(job.Message, legacyPluginJobMessagePrefix)), &env); err != nil {
		return "", "", "", "", nil, false
	}
	return env.PluginID, env.Key, env.RuntimeName, env.Description, clonePayload(env.Payload), env.PluginID != "" && env.Key != ""
}

func IsPluginJob(job Job) bool {
	return job.OwnerKind == JobOwnerPlugin
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
