package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"golang.org/x/oauth2"

	"github.com/jackc/pgx/v5/pgxpool"

	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/version"
	"github.com/CherryHQ/stella/internal/plugin"
	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/resources"
)

type pluginSetup struct {
	catalog                *plugin.Catalog
	host                   *pluginhost.Host
	channelRuntimeServices *pluginhost.ChannelPlatform
	oauthRegistry          *oauth.ProviderRegistry
	manifestToReconcile    *manifest.Manifest
}

func setupPlugins(ctx context.Context, db *pgxpool.Pool, store config.Store, dispatcher *notify.Dispatcher) (*pluginSetup, error) {
	oidcStore := appdb.NewOIDCStore(db)
	channelRuntimeServices := pluginhost.NewChannelRuntimeServices()
	channelRuntimeServices.SetBuildVersion(version.Version)
	channelRuntimeServices.Set(ctx, nil, nil, nil)
	stateStore := pluginhost.NewStateStore(db)

	phost := pluginhost.New(store,
		pluginhost.WithAuthService(pluginhost.NewAuthService(oidcStore)),
		pluginhost.WithNotificationService(dispatcher),
		pluginhost.WithStateStore(stateStore),
		pluginhost.WithChannelRuntimeServices(channelRuntimeServices),
	)

	code := pkgplugins.NewCatalog()
	for _, id := range pkgplugins.Names() {
		implementation, ok := pkgplugins.Get(id)
		if !ok {
			return nil, fmt.Errorf("missing shipped plugin %q", id)
		}
		code.Register(id, implementation)
	}
	if err := phost.LoadCatalog(code); err != nil {
		return nil, fmt.Errorf("load plugin catalog: %w", err)
	}

	catalog := plugin.NewCatalog()
	codeDefinitions, err := phost.BuiltinDefinitions(code)
	if err != nil {
		return nil, err
	}
	cliDefinitions, err := manifest.BuiltinDefinitions()
	if err != nil {
		return nil, err
	}
	toolDefinitions, err := pluginhost.BuiltinToolDefinitions(newToolMetaRegistry(generatedFamilies()...))
	if err != nil {
		return nil, err
	}
	bundledRuntimeDefinitions, err := pluginhost.BuiltinBundledRuntimeDefinitions()
	if err != nil {
		return nil, err
	}
	owners := make(map[string]struct{})
	for _, definitions := range [][]plugin.Definition{codeDefinitions, cliDefinitions, toolDefinitions, bundledRuntimeDefinitions} {
		for _, definition := range definitions {
			if err := catalog.Register(definition); err != nil {
				return nil, err
			}
			owners[definition.ID] = struct{}{}
		}
	}
	bundled, err := resources.Default()
	if err != nil {
		return nil, err
	}
	if err := bundled.ValidateBuiltinSkillOwners(owners); err != nil {
		return nil, err
	}

	var (
		oauthRegistry       *oauth.ProviderRegistry
		manifestToReconcile *manifest.Manifest
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
		catalog:                catalog,
		host:                   phost,
		channelRuntimeServices: channelRuntimeServices,
		oauthRegistry:          oauthRegistry,
		manifestToReconcile:    manifestToReconcile,
	}, nil
}

func loadBuiltinManifestWithOverrides(ctx context.Context, store config.Store) (*manifest.Manifest, error) {
	builtin, err := manifest.LoadBuiltin()
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
	rows := make([]manifest.StoredOverride, 0, len(overrides))
	for _, ov := range overrides {
		rows = append(rows, manifest.StoredOverride{
			PluginID: ov.PluginID,
			Enabled:  ov.Enabled,
			Config:   ov.Config,
		})
	}
	// The same resolve the admin API uses. Applying only the enable flag here is
	// what used to make a customization evaporate on restart: the plugin host and
	// the binary reconcile would both be handed the untouched builtin.
	return manifest.Resolve(builtin, rows, func(id string, err error) {
		slog.Warn("manifest plugin: ignoring corrupt override", "plugin", id, "error", err)
	}), nil
}

func buildOAuthRegistry(merged *manifest.Manifest) *oauth.ProviderRegistry {
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

func reconcileManifestPluginsInBackground(ctx context.Context, wg *sync.WaitGroup, m *manifest.Manifest, stellaHome string) {
	wg.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("manifest plugin reconcile panic", "panic", r)
			}
		}()
		slog.Info("manifest plugin reconcile queued in background")
		manifest.Reconcile(ctx, m, stellaHome)
	})
}
