package pluginhost

import (
	webhookplugin "github.com/CherryHQ/stella/plugins/channels/webhook"
)

// WebhookRunConfig is the behaviour-only view of a webhook channel's persisted
// config that the inbound-ingress transport needs. It exposes exactly the knobs
// the HTTP handler consumes, with the effective (defaulted) timeouts already
// resolved here, so internal/server never imports the webhook plugin.
type WebhookRunConfig struct {
	// MaxRunTimeoutSeconds is the effective hard ceiling on the agent run.
	MaxRunTimeoutSeconds int
	// WaitTimeoutSeconds is the effective synchronous-reply wait budget.
	WaitTimeoutSeconds int
}

// DecodeWebhookRunConfig decodes a webhook channel config map into the
// behaviour-only view consumed by the webhook ingress handler. The plugin owns
// the decode + default-resolution rules; the transport receives only the
// resolved values.
func (h *Host) DecodeWebhookRunConfig(cfg map[string]any) (WebhookRunConfig, error) {
	c, err := webhookplugin.DecodeConfig(cfg)
	if err != nil {
		return WebhookRunConfig{}, err
	}
	return WebhookRunConfig{
		MaxRunTimeoutSeconds: c.EffectiveMaxRunTimeout(),
		WaitTimeoutSeconds:   c.EffectiveWaitTimeout(),
	}, nil
}
