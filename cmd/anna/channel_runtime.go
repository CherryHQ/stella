package main

import (
	"context"
	"log/slog"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type managedChannelRuntimeHost interface {
	ListRegisteredPlugins() []pkgplugins.PluginMeta
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
	for _, meta := range host.ListRegisteredPlugins() {
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
