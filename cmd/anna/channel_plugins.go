package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/pluginapi"
	"github.com/vaayne/anna/internal/pluginhost"
)

type pluginChannel struct {
	adapter *pluginhost.ChannelAdapter
}

func (c *pluginChannel) Name() string { return c.adapter.Name() }

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

func loadBundledChannelCatalog(workDir string) (*pluginhost.Catalog, error) {
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
	if err := catalog.Merge(channel.BuiltinChannelDefinitions(workDir, config.AnnaHome())...); err != nil {
		return nil, err
	}
	return catalog, nil
}

func newChannelPlugin(name string, def pluginhost.Definition) channel.Channel {
	return &pluginChannel{adapter: pluginhost.NewChannelAdapter(def, pluginhost.SupervisorOptions{Logger: slog.Default().With("channel", name)})}
}
