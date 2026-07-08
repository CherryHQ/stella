// Package webhook registers the inbound-only webhook channel.
//
// Unlike the bot channels (telegram/feishu/…), the webhook channel has no
// long-running runtime: it is a plain channel type whose ingress is an HTTP
// endpoint (POST /webhooks/{id}) served by internal/server. This plugin exists
// only to provide the admin config schema + validation and to make "webhook" a
// selectable channel type; it registers no runtime, so ApplyChannel is a no-op
// for webhook instances.
package webhook

import (
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const PluginID = "channel/webhook"

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           PluginID,
			Kind:         "channel",
			Name:         pkgchannel.PlatformWebhook,
			DisplayName:  "Webhook",
			Description:  "Inbound-only webhook: POST to trigger an agent. No outbound.",
			Managed:      false,
			AdminVisible: true,
			HasConfig:    true,
			Capabilities: []string{
				pkgplugins.CapabilityChannel,
				pkgplugins.CapabilityConfig,
			},
		})
		host.AddAdmin(pkgplugins.AdminSpec{
			PluginID:      PluginID,
			DefaultConfig: func() map[string]any { return map[string]any{} },
			Schema:        configSchema(),
			Validate:      Validate,
			Redact:        RedactConfig,
		})
		host.AddChannel(pkgplugins.ChannelSpec{
			PluginID: PluginID,
			Name:     pkgchannel.PlatformWebhook,
			// Any saved instance is usable; behaviour is validated by Validate.
			Configured: func(raw map[string]any) bool { return true },
			// No Build: the webhook has no channel.Channel runtime.
		})
	}))
}

func configSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"default_wait": map[string]any{
				"type":        "boolean",
				"description": "Wait for the agent reply by default (synchronous). Overridable per request with ?wait=true|false.",
			},
			"wait_timeout_seconds": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "How long a synchronous caller waits before a 504 (the run continues in the background). Default 60.",
			},
			"max_run_timeout_seconds": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Hard ceiling on the agent run. Default 300.",
			},
			"session_mode": map[string]any{
				"type":        "string",
				"enum":        []any{pkgchannel.WebhookSessionEphemeral, pkgchannel.WebhookSessionPersistent},
				"description": "ephemeral: fresh session per trigger (default). persistent: one stable session per webhook.",
			},
		},
	}
}
