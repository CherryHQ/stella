package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	cfgstore "github.com/CherryHQ/stella/cmd/stellad/store"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/version"
	"github.com/CherryHQ/stella/internal/plugin"
	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/toolmeta"
	"github.com/CherryHQ/stella/resources"
)

type pluginSetup struct {
	catalog                *plugin.Catalog
	host                   *pluginhost.Host
	channelRuntimeServices *pluginhost.ChannelPlatform
	oauthRegistry          *oauth.ProviderRegistry
	nativePolicy           *plugin.NativePolicy
	nativeRegistry         plugin.NativeRegistry
	bundled                *resources.Registry
}

// nativeRegistry is composed only from Go-owned registration and generated
// tool metadata. Manifest IDs deliberately never enter this set.
func nativeRegistry(code *pkgplugins.Catalog, families ...[]toolmeta.ActionTool) plugin.NativeRegistry {
	registered := make(plugin.NativeRegistryMap)
	if code != nil {
		for _, id := range code.Names() {
			defaultEnabled := true
			if builtin, ok := config.BuiltinPluginByID(id); ok {
				defaultEnabled = builtin.DefaultEnabled
			}
			registered[id] = defaultEnabled
		}
	}
	for _, family := range families {
		for _, tool := range family {
			if tool.PluginID == "" {
				continue
			}
			if _, ok := registered[tool.PluginID]; !ok {
				registered[tool.PluginID] = true
			}
		}
	}
	return registered
}

func setupPlugins(ctx context.Context, db *pgxpool.Pool, store config.Store, dispatcher *notify.Dispatcher) (*pluginSetup, error) {
	oidcStore := appdb.NewOIDCStore(db)
	channelRuntimeServices := pluginhost.NewChannelRuntimeServices()
	channelRuntimeServices.SetBuildVersion(version.Version)
	channelRuntimeServices.Set(ctx, nil, nil, nil)
	stateStore := pluginhost.NewStateStore(db)

	phostOpts := []pluginhost.Option{
		pluginhost.WithAuthService(pluginhost.NewAuthService(oidcStore)),
		pluginhost.WithNotificationService(dispatcher),
		pluginhost.WithStateStore(stateStore),
		pluginhost.WithChannelRuntimeServices(channelRuntimeServices),
	}
	if os.Getenv("STELLA_CHANNEL_DURABLE_INGRESS") != "" {
		// Durable channel path: replicas compete for channel ownership through
		// the DB lease; only the holder runs the poller and sends.
		phostOpts = append(phostOpts, pluginhost.WithChannelLeases(db, replicaID()))
	}
	phost := pluginhost.New(store, phostOpts...)

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
	cliDefinitions, err := plugin.BuiltinDefinitions()
	if err != nil {
		return nil, err
	}
	nativeIDs := nativeRegistry(code, generatedFamilies()...)
	owners := make(map[string]struct{})
	for _, definition := range cliDefinitions {
		if err := catalog.Register(definition); err != nil {
			return nil, err
		}
		owners[definition.ID] = struct{}{}
	}
	bundled, err := resources.Default()
	if err != nil {
		return nil, err
	}
	if err := bundled.ValidateBuiltinSkillOwners(owners); err != nil {
		return nil, err
	}

	oauthRegistry, err := oauth.NewBuiltinRegistry(resources.BuiltinOAuthYAML())
	if err != nil {
		return nil, fmt.Errorf("load shipped OAuth definitions: %w", err)
	}

	nativeStore := cfgstore.NewDBStore(db)
	nativePolicy := plugin.NewNativePolicy(nativeStore, nativeIDs)
	phost.SetNativePolicy(nativePolicy)
	return &pluginSetup{
		catalog:                catalog,
		host:                   phost,
		channelRuntimeServices: channelRuntimeServices,
		oauthRegistry:          oauthRegistry,
		nativePolicy:           nativePolicy,
		nativeRegistry:         nativeIDs,
		bundled:                bundled,
	}, nil
}

// replicaID names this process for cross-replica fencing (channel leases, run
// worker ids). Per-process unique, stable for the process lifetime.
func replicaID() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), uuid.Must(uuid.NewV7()).String()[:8])
}
