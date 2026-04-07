package mcp

import (
	"context"

	"github.com/vaayne/anna/internal/config"
)

const PluginName = "mcp"

func PluginID() string {
	return config.PluginID(config.PluginKindTool, PluginName)
}

func LoadPluginState(ctx context.Context, store config.Store) (Config, bool, error) {
	plugin, err := store.GetPlugin(ctx, PluginID())
	if err != nil {
		return Config{}, false, err
	}
	cfg, err := DecodeConfig(plugin.Config)
	if err != nil {
		return Config{}, plugin.Enabled, err
	}
	return cfg, plugin.Enabled, nil
}
