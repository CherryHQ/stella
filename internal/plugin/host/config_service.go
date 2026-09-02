package host

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type configService struct {
	store config.Store
}

func (s *configService) Get(ctx context.Context, pluginID string) (pkgplugins.PluginState, error) {
	plug, err := s.store.GetPlugin(ctx, pluginID)
	if err == nil {
		return pkgplugins.PluginState{ID: pluginID, Enabled: plug.Enabled, Config: cloneMap(plug.Config)}, nil
	}
	return pkgplugins.PluginState{}, fmt.Errorf("get plugin state %q: %w", pluginID, err)
}

func (s *configService) Set(ctx context.Context, pluginID string, raw map[string]any) error {
	return s.store.SetPluginConfig(ctx, pluginID, cloneMap(raw))
}

func (s *configService) SetEnabled(ctx context.Context, pluginID string, enabled bool) error {
	return s.store.SetPluginEnabled(ctx, pluginID, enabled)
}
