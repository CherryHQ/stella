package controlplane

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// weixinFakeStore records the credential mirror's writes. Everything else on
// config.Store stays nil: the mirror touches nothing else.
type weixinFakeStore struct {
	config.Store
	mirrored []config.Plugin
	upserted int
}

func (f *weixinFakeStore) SetChannelPluginConfig(_ context.Context, id, kind, name string, cfg map[string]any) error {
	f.mirrored = append(f.mirrored, config.Plugin{ID: id, Kind: kind, Name: name, Config: cfg})
	return nil
}

// A whole-row write here would be the bug: it carries an enabled value.
func (f *weixinFakeStore) UpsertPlugin(context.Context, config.Plugin) error {
	f.upserted++
	return nil
}

// The enabled column is the admin kill switch and lives on the same row as the
// credentials. The mirror must reach the config column alone — asserted here by
// the call it makes, and in SQL by UpsertPluginConfig's ON CONFLICT clause.
func TestMirrorWeixinPluginConfigWritesConfigOnly(t *testing.T) {
	store := &weixinFakeStore{}
	svc := NewService(store, nil, nil, nil, nil)
	cfg := map[string]any{"app_id": "a1"}
	if err := svc.mirrorWeixinPluginConfig(context.Background(), pkgchannel.PlatformWeixin, cfg); err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if store.upserted != 0 {
		t.Fatalf("mirror wrote %d whole rows, want 0", store.upserted)
	}
	if len(store.mirrored) != 1 {
		t.Fatalf("config writes = %d, want 1", len(store.mirrored))
	}
	got := store.mirrored[0]
	if want := config.PluginID(config.PluginKindChannel, pkgchannel.PlatformWeixin); got.ID != want {
		t.Errorf("id = %q, want %q", got.ID, want)
	}
	if got.Kind != config.PluginKindChannel || got.Name != pkgchannel.PlatformWeixin {
		t.Errorf("kind/name = %q/%q", got.Kind, got.Name)
	}
	if got.Config["app_id"] != "a1" {
		t.Errorf("config not mirrored: %v", got.Config)
	}
}

func TestMirrorWeixinPluginConfigIgnoresOtherPlatforms(t *testing.T) {
	store := &weixinFakeStore{}
	svc := NewService(store, nil, nil, nil, nil)
	if err := svc.mirrorWeixinPluginConfig(context.Background(), pkgchannel.PlatformTelegram, map[string]any{"token": "t"}); err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if len(store.mirrored) != 0 || store.upserted != 0 {
		t.Fatalf("telegram wrote %d config rows and %d whole rows, want 0 and 0", len(store.mirrored), store.upserted)
	}
}
