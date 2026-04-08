package channel

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/vaayne/anna/internal/config"
)

func LoadConfig[T any](store config.Store, channelID string) *T {
	pluginID := config.PluginID(config.PluginKindChannel, channelID)
	p, err := store.GetPlugin(context.Background(), pluginID)
	if err != nil || !p.Enabled {
		return nil
	}

	data, err := json.Marshal(p.Config)
	if err != nil {
		slog.Warn("failed to marshal plugin config", "plugin", pluginID, "error", err)
		return nil
	}
	var cfg T
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("failed to parse plugin config", "plugin", pluginID, "error", err)
		return nil
	}
	return &cfg
}
