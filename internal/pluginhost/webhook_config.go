package pluginhost

import (
	webhookplugin "github.com/CherryHQ/stella/plugins/channels/webhook"
)

// WebhookRunConfig is the behaviour-only view of a webhook channel's persisted
// config that the inbound-ingress transport needs. It exposes exactly the knobs
// the HTTP handler consumes, with the effective (defaulted) timeouts already
// resolved here, so internal/server never imports the webhook plugin.
type WebhookEndpointConfig struct {
	Provider           string
	GitHubEvents       []string
	GitHubRepositories []string
}

type WebhookRunConfig struct {
	// DefaultWait selects synchronous (true) vs. fire-and-forget (false) when a
	// request does not set the ?wait query parameter.
	DefaultWait bool
	// Persistent reports whether triggers accumulate into one stable session.
	Persistent bool
	// MaxRunTimeoutSeconds is the effective hard ceiling on the agent run.
	MaxRunTimeoutSeconds int
	// WaitTimeoutSeconds is the effective synchronous-reply wait budget.
	WaitTimeoutSeconds int
}

// DecodeWebhookEndpointConfig exposes the non-secret provider policy needed by
// control-plane endpoint issuance. The plugin remains the single decoder for
// persisted webhook behavior; no provider secret can enter this view.
func (h *Host) DecodeWebhookEndpointConfig(cfg map[string]any) (WebhookEndpointConfig, error) {
	c, err := webhookplugin.DecodeConfig(cfg)
	if err != nil {
		return WebhookEndpointConfig{}, err
	}
	return WebhookEndpointConfig{
		Provider:           c.Provider,
		GitHubEvents:       append([]string(nil), c.GitHubEvents...),
		GitHubRepositories: append([]string(nil), c.GitHubRepositories...),
	}, nil
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
		DefaultWait:          c.DefaultWait,
		Persistent:           c.Persistent(),
		MaxRunTimeoutSeconds: c.EffectiveMaxRunTimeout(),
		WaitTimeoutSeconds:   c.EffectiveWaitTimeout(),
	}, nil
}
