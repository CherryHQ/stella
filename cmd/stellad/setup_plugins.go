package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"golang.org/x/oauth2"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/pluginstate"
	skills "github.com/CherryHQ/stella/internal/skills"
)

type pluginSetup struct {
	host                   *pluginhost.Host
	channelRuntimeServices *pluginhost.ChannelPlatform
	oauthRegistry          *oauth.ProviderRegistry
	manifestToReconcile    *manifestplugins.Manifest
}

func setupPlugins(ctx context.Context, db *pgxpool.Pool, store config.Store, skillStore *skills.DiskSyncStore, dispatcher *notify.Dispatcher) (*pluginSetup, error) {
	oidcStore := appdb.NewOIDCStore(db)
	channelRuntimeServices := pluginhost.NewChannelRuntimeServices()
	stateStore := pluginstate.New(db)

	phost := pluginhost.New(store,
		pluginhost.WithAuthService(pluginhost.NewAuthService(oidcStore)),
		pluginhost.WithNotificationService(dispatcher),
		pluginhost.WithStateStore(stateStore),
		pluginhost.WithSkillStore(skillStore),
		pluginhost.WithChannelRuntimeServices(channelRuntimeServices),
	)

	if err := phost.LoadDefaultCatalog(); err != nil {
		return nil, fmt.Errorf("load plugin catalog: %w", err)
	}

	var (
		oauthRegistry       *oauth.ProviderRegistry
		manifestToReconcile *manifestplugins.Manifest
	)

	builtinManifest, err := loadBuiltinManifestWithOverrides(ctx, store)
	if err != nil {
		slog.Warn("manifest plugin: failed to load builtin manifest", "error", err)
	} else {
		manifestToReconcile = builtinManifest
		phost.RegisterManifestPlugins(builtinManifest)
		oauthRegistry = buildOAuthRegistry(builtinManifest)
	}

	return &pluginSetup{
		host:                   phost,
		channelRuntimeServices: channelRuntimeServices,
		oauthRegistry:          oauthRegistry,
		manifestToReconcile:    manifestToReconcile,
	}, nil
}

func loadBuiltinManifestWithOverrides(ctx context.Context, store config.Store) (*manifestplugins.Manifest, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	if store == nil {
		return builtin, nil
	}
	overrides, err := store.ListManifestPluginOverrides(ctx)
	if err != nil {
		return nil, fmt.Errorf("list manifest plugin overrides: %w", err)
	}
	byID := make(map[string]config.ManifestPluginOverride, len(overrides))
	for _, ov := range overrides {
		byID[ov.PluginID] = ov
	}
	for i := range builtin.Plugins {
		if ov, ok := byID[builtin.Plugins[i].ID]; ok && ov.Enabled != nil {
			builtin.Plugins[i].Enabled = *ov.Enabled
		}
	}
	return builtin, nil
}

func buildOAuthRegistry(merged *manifestplugins.Manifest) *oauth.ProviderRegistry {
	registry := oauth.NewProviderRegistry()
	for _, op := range merged.OAuthProviders {
		flows := make([]oauth.ProviderFlowConfig, 0, len(op.Flows))
		for _, f := range op.Flows {
			var authStyle oauth2.AuthStyle
			switch f.AuthStyle {
			case "in_params":
				authStyle = oauth2.AuthStyleInParams
			case "in_header":
				authStyle = oauth2.AuthStyleInHeader
			default:
				authStyle = oauth2.AuthStyleAutoDetect
			}
			flows = append(flows, oauth.ProviderFlowConfig{
				Type:          f.Type,
				AuthURL:       f.AuthURL,
				DeviceAuthURL: f.DeviceAuthURL,
				TokenURL:      f.TokenURL,
				AuthStyle:     authStyle,
				PKCE:          f.PKCE,
			})
		}
		registry.Register(oauth.ProviderConfig{
			ID:           op.ID,
			Icon:         op.Icon,
			Scopes:       op.Scopes,
			VaultKey:     op.VaultKey,
			Flows:        flows,
			ClientID:     op.ClientID,
			ClientSecret: op.ClientSecret,
		})
	}
	return registry
}

func reconcileManifestPluginsInBackground(ctx context.Context, wg *sync.WaitGroup, m *manifestplugins.Manifest, stellaHome string) {
	wg.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("manifest plugin reconcile panic", "panic", r)
			}
		}()
		slog.Info("manifest plugin reconcile queued in background")
		manifestplugins.Reconcile(ctx, m, stellaHome)
	})
}
