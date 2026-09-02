package channel

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/CherryHQ/stella/internal/platform/config"
)

func LoadConfig[T any](store config.Store, channelID string) *T {
	ch, err := store.GetChannel(context.Background(), channelID)
	if err != nil || !ch.Enabled {
		return nil
	}

	var cfg T
	if err := json.Unmarshal([]byte(ch.Config), &cfg); err != nil {
		slog.Warn("failed to parse channel config", "channel", channelID, "error", err)
		return nil
	}
	return &cfg
}
