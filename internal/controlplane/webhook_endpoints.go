package controlplane

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/webhook"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// GetWebhookEndpoint returns secret-safe metadata for the channel's singleton
// endpoint. Access already passed the admin gate, so endpoint existence is never
// observable by a non-admin caller.
func (a *Access) GetWebhookEndpoint(ctx context.Context, channelID string) (webhook.Endpoint, error) {
	if _, err := a.webhookChannel(ctx, channelID); err != nil {
		return webhook.Endpoint{}, err
	}
	svc, err := a.requireEndpointService()
	if err != nil {
		return webhook.Endpoint{}, err
	}
	endpoint, err := svc.GetByChannel(ctx, channelID)
	return endpoint, endpointError(err)
}

// IssueWebhookEndpoint binds an owner to the channel's current Agent. Provider
// policy comes from the persisted webhook plugin config; the request cannot
// smuggle a GitHub allowlist or secret around that boundary.
func (a *Access) IssueWebhookEndpoint(ctx context.Context, channelID, ownerID string, provider webhook.Provider) (webhook.IssueResult, webhook.GitHubPolicy, error) {
	ch, err := a.webhookChannel(ctx, channelID)
	if err != nil {
		return webhook.IssueResult{}, webhook.GitHubPolicy{}, err
	}
	policy, err := a.webhookPolicy(ch, provider)
	if err != nil {
		return webhook.IssueResult{}, webhook.GitHubPolicy{}, err
	}
	svc, err := a.requireEndpointService()
	if err != nil {
		return webhook.IssueResult{}, webhook.GitHubPolicy{}, err
	}
	result, err := svc.Issue(ctx, webhook.IssueRequest{
		ChannelID: channelID, OwnerUserID: ownerID, Provider: provider, GitHub: policy,
		ExpectedChannelConfig: &ch.Config,
	})
	return result, policy, endpointError(err)
}

func (a *Access) RotateWebhookEndpoint(ctx context.Context, channelID string) (webhook.RotationResult, webhook.GitHubPolicy, error) {
	ch, err := a.webhookChannel(ctx, channelID)
	if err != nil {
		return webhook.RotationResult{}, webhook.GitHubPolicy{}, err
	}
	svc, err := a.requireEndpointService()
	if err != nil {
		return webhook.RotationResult{}, webhook.GitHubPolicy{}, err
	}
	endpoint, err := svc.GetByChannel(ctx, channelID)
	if err != nil {
		return webhook.RotationResult{}, webhook.GitHubPolicy{}, endpointError(err)
	}
	policy, err := a.webhookPolicy(ch, endpoint.Provider)
	if err != nil {
		return webhook.RotationResult{}, webhook.GitHubPolicy{}, err
	}
	result, err := svc.Rotate(ctx, endpoint.ID)
	return result, policy, endpointError(err)
}

func (a *Access) DeleteWebhookEndpoint(ctx context.Context, channelID string) error {
	if _, err := a.webhookChannel(ctx, channelID); err != nil {
		return err
	}
	svc, err := a.requireEndpointService()
	if err != nil {
		return err
	}
	endpoint, err := svc.GetByChannel(ctx, channelID)
	if err != nil {
		return endpointError(err)
	}
	deleted, err := svc.Delete(ctx, endpoint.ID)
	if err != nil {
		return endpointError(err)
	}
	if !deleted {
		return notFound("webhook endpoint not found")
	}
	return nil
}

func (a *Access) webhookChannel(ctx context.Context, channelID string) (config.Channel, error) {
	ch, err := a.GetChannel(ctx, channelID)
	if err != nil {
		return config.Channel{}, err
	}
	if effectiveChannelType(ch) != pkgchannel.PlatformWebhook {
		return config.Channel{}, invalid("channel is not a webhook")
	}
	return ch, nil
}

// WebhookPolicy returns the non-secret configured GitHub allowlists for an
// endpoint metadata response. It never reads endpoint verifier material.
func (a *Access) WebhookPolicy(ctx context.Context, channelID string, provider webhook.Provider) (webhook.GitHubPolicy, error) {
	ch, err := a.webhookChannel(ctx, channelID)
	if err != nil {
		return webhook.GitHubPolicy{}, err
	}
	return a.webhookPolicy(ch, provider)
}

func (a *Access) webhookPolicy(ch config.Channel, provider webhook.Provider) (webhook.GitHubPolicy, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(ch.Config), &raw); err != nil {
		return webhook.GitHubPolicy{}, invalid("invalid webhook config")
	}
	if raw == nil {
		raw = map[string]any{}
	}
	if a.svc.plugins == nil {
		return webhook.GitHubPolicy{}, unavailable("webhook endpoint service unavailable")
	}
	cfg, err := a.svc.plugins.DecodeWebhookEndpointConfig(raw)
	if err != nil {
		return webhook.GitHubPolicy{}, invalid("invalid webhook config")
	}
	if cfg.Provider != string(provider) {
		return webhook.GitHubPolicy{}, invalid("webhook provider must match channel config")
	}
	return webhook.GitHubPolicy{Events: cfg.GitHubEvents, Repositories: cfg.GitHubRepositories}, nil
}

func (a *Access) requireEndpointService() (*webhook.Service, error) {
	if a.svc.webhooks == nil {
		return nil, unavailable("webhook endpoint service unavailable")
	}
	return a.svc.webhooks, nil
}

func endpointError(err error) error {
	var bindingConflict *config.ChannelBindingConflictError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, webhook.ErrNotFound):
		return notFound("webhook endpoint not found")
	case errors.Is(err, webhook.ErrEndpointExists),
		errors.Is(err, webhook.ErrChannelEndpointActive),
		errors.Is(err, webhook.ErrChannelConfigChanged):
		return &ConflictError{Msg: "webhook endpoint is active; revoke it before changing the channel binding"}
	case errors.Is(err, webhook.ErrGitHubSecretUnavailable):
		return unavailable("system vault unavailable")
	case errors.As(err, &bindingConflict):
		return invalid(bindingConflict.Error())
	case errors.Is(err, webhook.ErrInvalidChannelID),
		errors.Is(err, webhook.ErrInvalidOwnerUserID),
		errors.Is(err, webhook.ErrInvalidProvider),
		errors.Is(err, webhook.ErrInvalidGitHubPolicy),
		errors.Is(err, webhook.ErrOwnerInactive),
		errors.Is(err, webhook.ErrOwnerAgentForbidden),
		errors.Is(err, webhook.ErrAgentDisabled),
		errors.Is(err, webhook.ErrChannelNotWebhook):
		return invalid(err.Error())
	default:
		return err
	}
}
