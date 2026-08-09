package controlplane

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// weixinFakeStore serves the one plugin row the Weixin mirror reads and records
// what it writes back. Everything else on config.Store stays nil: the mirror
// touches nothing else.
type weixinFakeStore struct {
	config.Store
	existing *config.Plugin
	upserted []config.Plugin
}

func (f *weixinFakeStore) ListPluginOverrides(context.Context) ([]config.Plugin, error) {
	if f.existing == nil {
		return nil, nil
	}
	return []config.Plugin{*f.existing}, nil
}

func (f *weixinFakeStore) UpsertPlugin(_ context.Context, p config.Plugin) error {
	f.upserted = append(f.upserted, p)
	return nil
}

func TestMirrorWeixinPluginConfigNeverReEnablesADisabledPlatform(t *testing.T) {
	id := config.PluginID(config.PluginKindChannel, pkgchannel.PlatformWeixin)
	cases := []struct {
		name        string
		existing    *config.Plugin
		wantEnabled bool
	}{
		{
			name:        "no override row keeps the absence's meaning",
			existing:    nil,
			wantEnabled: true,
		},
		{
			name:        "an admin's explicit off survives a channel save",
			existing:    &config.Plugin{ID: id, Enabled: false},
			wantEnabled: false,
		},
		{
			name:        "an explicit on stays on",
			existing:    &config.Plugin{ID: id, Enabled: true},
			wantEnabled: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &weixinFakeStore{existing: tc.existing}
			svc := NewService(store, nil, nil, nil, nil)
			cfg := map[string]any{"app_id": "a1"}
			if err := svc.mirrorWeixinPluginConfig(context.Background(), pkgchannel.PlatformWeixin, cfg); err != nil {
				t.Fatalf("mirror: %v", err)
			}
			if len(store.upserted) != 1 {
				t.Fatalf("upserts = %d, want 1", len(store.upserted))
			}
			got := store.upserted[0]
			if got.Enabled != tc.wantEnabled {
				t.Errorf("enabled = %v, want %v", got.Enabled, tc.wantEnabled)
			}
			if got.Config["app_id"] != "a1" {
				t.Errorf("config not mirrored: %v", got.Config)
			}
		})
	}
}

func TestMirrorWeixinPluginConfigIgnoresOtherPlatforms(t *testing.T) {
	store := &weixinFakeStore{}
	svc := NewService(store, nil, nil, nil, nil)
	if err := svc.mirrorWeixinPluginConfig(context.Background(), pkgchannel.PlatformTelegram, map[string]any{"token": "t"}); err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if len(store.upserted) != 0 {
		t.Fatalf("telegram wrote %d plugin rows, want 0", len(store.upserted))
	}
}
