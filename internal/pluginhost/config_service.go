package pluginhost

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/config"
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
	plug, err := s.store.GetPlugin(ctx, pluginID)
	if err != nil {
		return fmt.Errorf("get plugin %q: %w", pluginID, err)
	}
	if plug.Kind != config.PluginKindSandbox {
		return s.store.SetPluginEnabled(ctx, pluginID, enabled)
	}

	sandboxes, err := s.store.ListPluginsByKind(ctx, config.PluginKindSandbox)
	if err != nil {
		return fmt.Errorf("list sandbox plugins: %w", err)
	}
	if enabled {
		if err := s.store.SetPluginEnabled(ctx, pluginID, true); err != nil {
			return err
		}
		for _, sandbox := range sandboxes {
			if sandbox.ID == pluginID || !sandbox.Enabled {
				continue
			}
			if err := s.store.SetPluginEnabled(ctx, sandbox.ID, false); err != nil {
				return err
			}
		}
		return nil
	}

	for _, sandbox := range sandboxes {
		if sandbox.ID != pluginID && sandbox.Enabled {
			return s.store.SetPluginEnabled(ctx, pluginID, false)
		}
	}
	if pluginID == config.PluginID(config.PluginKindSandbox, config.SandboxBackendLocal) {
		return nil
	}
	if err := s.store.SetPluginEnabled(ctx, config.PluginID(config.PluginKindSandbox, config.SandboxBackendLocal), true); err != nil {
		return err
	}
	return s.store.SetPluginEnabled(ctx, pluginID, false)
}
