package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
	"github.com/vaayne/anna/internal/pluginapi"
	"github.com/vaayne/anna/internal/pluginhost"
)

type pluginChannel struct {
	name    string
	adapter *pluginhost.ChannelAdapter
}

func (c *pluginChannel) Name() string { return c.name }

func (c *pluginChannel) Start(ctx context.Context) error { return c.adapter.Start(ctx) }

func (c *pluginChannel) Stop() { c.adapter.Stop() }

func (c *pluginChannel) Notify(ctx context.Context, n channel.Notification) error {
	return c.adapter.Notify(ctx, pluginapi.ChannelNotification{
		Channel: n.Channel,
		ChatID:  n.ChatID,
		Text:    n.Text,
		Silent:  n.Silent,
	})
}

func loadRuntimeChannelCatalog(workDir, userDataDir string) (*pluginhost.Catalog, error) {
	if err := embedded.EnsurePlugins(config.AnnaHome()); err != nil {
		slog.Warn("failed to extract embedded plugins", "error", err)
	}

	roots := []string{}
	for _, root := range []string{config.BundledPluginsPath(), config.InstalledPluginsPath()} {
		if _, err := os.Stat(root); err == nil {
			roots = append(roots, root)
		}
	}
	catalog, err := pluginhost.Discover(roots...)
	if err != nil {
		return nil, err
	}
	return catalog, nil
}

func resolveChannelPluginDefinition(catalog *pluginhost.Catalog, name, workDir, userDataDir string) (pluginhost.Definition, error) {
	id := string(pluginapi.KindChannel) + "/" + name
	def, ok := catalog.Get(id)
	if !ok {
		return pluginhost.Definition{}, fmt.Errorf("channel %s: plugin %s not found", name, id)
	}
	if def.Manifest.Kind != pluginapi.KindChannel {
		return pluginhost.Definition{}, fmt.Errorf("channel %s bound to non-channel plugin %s", name, id)
	}
	// Inject runtime-specific args and metadata for subprocess plugins.
	if def.Manifest.Entrypoint == pluginhost.BuiltinEntrypoint {
		if workDir != "" {
			def.Manifest.Args = append(def.Manifest.Args, "--work-dir", workDir)
		}
		if userDataDir != "" {
			def.Manifest.Args = append(def.Manifest.Args, "--user-data-dir", userDataDir)
		}
		if def.Manifest.Metadata == nil {
			def.Manifest.Metadata = make(map[string]any)
		}
		def.Manifest.Metadata["work_dir"] = workDir
		def.Manifest.Metadata["user_data_dir"] = userDataDir
	}
	return def, nil
}

func newChannelPlugin(name string, def pluginhost.Definition) channel.Channel {
	return &pluginChannel{
		name:    name,
		adapter: pluginhost.NewChannelAdapter(def, pluginhost.SupervisorOptions{Logger: slog.Default().With("channel", name)}),
	}
}
