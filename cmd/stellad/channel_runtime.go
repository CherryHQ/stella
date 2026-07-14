package main

import (
	"context"
	"log/slog"

	"github.com/CherryHQ/stella/internal/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type managedChannelRuntimeHost interface {
	ListRegisteredPlugins() []pkgplugins.PluginInfo
	ListChannels(context.Context) ([]config.Channel, error)
	ApplyChannel(context.Context, config.Channel) error
	ChannelInstanceConfigured(config.Channel) bool
	ApplyPlugin(context.Context, string) error
	ChannelConfigured(context.Context, string) bool
}

type managedChannelRuntimeSummary struct {
	Registered int
	Configured int
	Started    int
}

func applyManagedChannelPlugins(ctx context.Context, host managedChannelRuntimeHost) managedChannelRuntimeSummary {
	if host == nil {
		return managedChannelRuntimeSummary{}
	}

	var summary managedChannelRuntimeSummary
	channels, err := host.ListChannels(ctx)
	if err == nil && len(channels) > 0 {
		summary.Registered = len(channels)
		for _, ch := range channels {
			if ctx.Err() != nil {
				return summary
			}
			configured := host.ChannelInstanceConfigured(ch)
			if configured {
				summary.Configured++
			}
			if err := host.ApplyChannel(ctx, ch); err != nil {
				slog.Warn("managed channel runtime failed to start", "channel_id", ch.ID, "channel_type", ch.Type, "error", err)
				continue
			}
			if configured {
				summary.Started++
			}
		}
		return summary
	}
	if err != nil {
		slog.Warn("failed to list channel instances; falling back to plugin channel startup", "error", err)
	}

	for _, meta := range host.ListRegisteredPlugins() {
		if ctx.Err() != nil {
			return summary
		}
		if meta.Kind != "channel" {
			continue
		}

		summary.Registered++
		configured := host.ChannelConfigured(ctx, meta.Name)
		if configured {
			summary.Configured++
		}

		if err := host.ApplyPlugin(ctx, meta.ID); err != nil {
			slog.Warn("managed channel runtime failed to start", "plugin_id", meta.ID, "channel", meta.Name, "error", err)
			continue
		}
		if configured {
			summary.Started++
		}
	}

	return summary
}
