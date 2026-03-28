package config

import (
	"context"
	"encoding/json"
	"fmt"
)

const runtimePluginsSettingKey = "runtime_plugins"

var defaultRuntimeToolBindings = map[string]string{
	"read":     "tool/read",
	"bash":     "tool/bash",
	"edit":     "tool/edit",
	"write":    "tool/write",
	"webfetch": "tool/webfetch",
}

var defaultRuntimeChannelBindings = map[string]string{
	"telegram": "channel/telegram",
	"qq":       "channel/qq",
	"feishu":   "channel/feishu",
	"weixin":   "channel/weixin",
}

// RuntimePluginBindings configures which subprocess plugin handles each
// built-in tool or channel slot. The separate setting keeps the legacy JS
// plugin list untouched while runtime plugin bindings evolve independently.
type RuntimePluginBindings struct {
	Tools    map[string]string `json:"tools,omitempty"`
	Channels map[string]string `json:"channels,omitempty"`
}

func DefaultRuntimePluginBindings() RuntimePluginBindings {
	return RuntimePluginBindings{
		Tools:    copyStringMap(defaultRuntimeToolBindings),
		Channels: copyStringMap(defaultRuntimeChannelBindings),
	}
}

func (b RuntimePluginBindings) ToolBinding(name string) string {
	if id := b.Tools[name]; id != "" {
		return id
	}
	return defaultRuntimeToolBindings[name]
}

func (b RuntimePluginBindings) ChannelBinding(name string) string {
	if id := b.Channels[name]; id != "" {
		return id
	}
	return defaultRuntimeChannelBindings[name]
}

func (b RuntimePluginBindings) EffectiveTools() map[string]string {
	out := copyStringMap(defaultRuntimeToolBindings)
	for name, id := range b.Tools {
		if id != "" {
			out[name] = id
		}
	}
	return out
}

func (b RuntimePluginBindings) EffectiveChannels() map[string]string {
	out := copyStringMap(defaultRuntimeChannelBindings)
	for name, id := range b.Channels {
		if id != "" {
			out[name] = id
		}
	}
	return out
}

func LoadRuntimePluginBindings(store Store) (RuntimePluginBindings, error) {
	if store == nil {
		return DefaultRuntimePluginBindings(), nil
	}

	val, err := store.GetSetting(context.Background(), runtimePluginsSettingKey)
	if err != nil || val == "" {
		return DefaultRuntimePluginBindings(), nil
	}

	bindings := DefaultRuntimePluginBindings()
	if err := json.Unmarshal([]byte(val), &bindings); err != nil {
		return RuntimePluginBindings{}, fmt.Errorf("parse %s: %w", runtimePluginsSettingKey, err)
	}
	return bindings, nil
}

func SaveRuntimePluginBindings(store Store, bindings RuntimePluginBindings) error {
	data, err := json.Marshal(bindings)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", runtimePluginsSettingKey, err)
	}
	return store.SetSetting(context.Background(), runtimePluginsSettingKey, string(data))
}

func copyStringMap(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
