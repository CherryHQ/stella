package pluginhost

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/internal/config"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type configService struct {
	store   config.Store
	aliases map[string]string // canonical plugin id -> legacy settings_plugins id
}

func (s *configService) rowID(pluginID string) string {
	if rowID, ok := s.aliases[pluginID]; ok {
		return rowID
	}
	return pluginID
}

func (s *configService) Get(ctx context.Context, pluginID string) (pkgplugins.PluginState, error) {
	rowID := s.rowID(pluginID)
	plug, err := s.store.GetPlugin(ctx, rowID)
	if err == nil {
		return pkgplugins.PluginState{ID: pluginID, Enabled: plug.Enabled, Config: cloneMap(plug.Config)}, nil
	}
	if rowID == pluginID && !strings.Contains(pluginID, "/") {
		legacyID := inferLegacyID(pluginID)
		if legacyID != "" {
			plug, legacyErr := s.store.GetPlugin(ctx, legacyID)
			if legacyErr == nil {
				s.aliases[pluginID] = legacyID
				return pkgplugins.PluginState{ID: pluginID, Enabled: plug.Enabled, Config: cloneMap(plug.Config)}, nil
			}
		}
	}
	return pkgplugins.PluginState{}, fmt.Errorf("get plugin state %q: %w", pluginID, err)
}

func (s *configService) Set(ctx context.Context, pluginID string, raw map[string]any) error {
	return s.store.SetPluginConfig(ctx, s.rowID(pluginID), cloneMap(raw))
}

func (s *configService) SetEnabled(ctx context.Context, pluginID string, enabled bool) error {
	return s.store.SetPluginEnabled(ctx, s.rowID(pluginID), enabled)
}

func inferLegacyID(pluginID string) string {
	switch pluginID {
	case "mcp":
		return config.PluginID(config.PluginKindTool, "mcp")
	default:
		return ""
	}
}
