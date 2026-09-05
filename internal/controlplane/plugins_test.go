package controlplane

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
)

func TestLegacyPluginAccessRejectsManifestIDs(t *testing.T) {
	store := &manifestPluginAccessStore{plugin: config.Plugin{
		ID: "tool/lark-cli", Kind: config.PluginKindTool, Name: "lark-cli", Enabled: true,
		Config: map[string]any{"legacy": "keep"},
	}}
	host := pluginhost.New(store)
	host.RegisterManifestPlugins(&manifest.Manifest{Plugins: []manifest.ManifestPlugin{{
		ID: "tool/lark-cli", Kind: config.PluginKindTool, Enabled: true,
		ManifestPluginDefinition: manifest.ManifestPluginDefinition{Name: "lark-cli"},
	}}})
	access, err := NewService(store, host, nil, nil, nil, nil).Begin(t.Context(), adminAuthority(t))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		call func() error
	}{
		{name: "status", call: func() error {
			_, err := access.GetPluginStatus(t.Context(), config.PluginKindTool, "lark-cli")
			return err
		}},
		{name: "config", call: func() error {
			_, err := access.GetPluginConfig(t.Context(), config.PluginKindTool, "lark-cli")
			return err
		}},
		{name: "schema", call: func() error {
			_, err := access.GetPluginConfigSchema(t.Context(), config.PluginKindTool, "lark-cli")
			return err
		}},
		{name: "toggle", call: func() error {
			_, err := access.TogglePlugin(t.Context(), config.PluginKindTool, "lark-cli", false)
			return err
		}},
		{name: "config update", call: func() error {
			_, err := access.UpdatePluginConfig(t.Context(), config.PluginKindTool, "lark-cli", map[string]any{"new": "value"})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if store.plugin.Enabled != true || store.plugin.Config["legacy"] != "keep" {
				t.Fatalf("legacy row changed after %s: %#v", test.name, store.plugin)
			}
		})
	}
}

type manifestPluginAccessStore struct {
	config.Store
	plugin config.Plugin
}

func (s *manifestPluginAccessStore) GetPlugin(_ context.Context, id string) (config.Plugin, error) {
	if id == s.plugin.ID {
		return s.plugin, nil
	}
	return config.Plugin{}, errors.New("unexpected plugin lookup")
}

func (s *manifestPluginAccessStore) SetPluginEnabled(_ context.Context, id string, enabled bool) error {
	if id == s.plugin.ID {
		s.plugin.Enabled = enabled
	}
	return nil
}

func (s *manifestPluginAccessStore) SetPluginConfig(_ context.Context, id string, cfg map[string]any) error {
	if id == s.plugin.ID {
		s.plugin.Config = cfg
	}
	return nil
}
