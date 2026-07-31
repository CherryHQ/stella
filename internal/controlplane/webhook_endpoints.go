package controlplane

import (
	"context"
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

// CreateWebhookEndpoint binds an owner to the channel's current Agent and mints
// a one-time capability. The endpoint fixes the identity scenario; the request
// carries the entire endpoint policy input.
func (a *Access) CreateWebhookEndpoint(ctx context.Context, channelID, ownerID string, provider webhook.Provider) (webhook.IssueResult, error) {
	if _, err := a.webhookChannel(ctx, channelID); err != nil {
		return webhook.IssueResult{}, err
	}
	svc, err := a.requireEndpointService()
	if err != nil {
		return webhook.IssueResult{}, err
	}
	result, err := svc.Issue(ctx, webhook.IssueRequest{
		ChannelID:   channelID,
		OwnerUserID: ownerID,
		Provider:    provider,
	})
	return result, endpointError(err)
}

// RotateWebhookEndpoint replaces the capability if expectedETag still matches
// the endpoint's current opaque etag (the compare-and-set precondition).
func (a *Access) RotateWebhookEndpoint(ctx context.Context, channelID string, expectedETag string) (webhook.RotationResult, error) {
	if _, err := a.webhookChannel(ctx, channelID); err != nil {
		return webhook.RotationResult{}, err
	}
	svc, err := a.requireEndpointService()
	if err != nil {
		return webhook.RotationResult{}, err
	}
	result, err := svc.Rotate(ctx, channelID, expectedETag)
	return result, endpointError(err)
}

// DeleteWebhookEndpoint revokes the channel's endpoint.
func (a *Access) DeleteWebhookEndpoint(ctx context.Context, channelID string) error {
	if _, err := a.webhookChannel(ctx, channelID); err != nil {
		return err
	}
	svc, err := a.requireEndpointService()
	if err != nil {
		return err
	}
	deleted, err := svc.Delete(ctx, channelID)
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

func (a *Access) requireEndpointService() (*webhook.Service, error) {
	if a.svc.webhooks == nil {
		return nil, unavailable("webhook endpoint service unavailable")
	}
	return a.svc.webhooks, nil
}

func endpointError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, webhook.ErrNotFound):
		return notFound("webhook endpoint not found")
	case errors.Is(err, webhook.ErrEndpointExists):
		return &ConflictError{Msg: "webhook endpoint is active; revoke it before issuing a new one"}
	case errors.Is(err, webhook.ErrStaleETag):
		return &ConflictError{Msg: "webhook endpoint changed since it was read; refresh and retry"}
	case errors.Is(err, webhook.ErrInvalidETag):
		return invalid("invalid etag")
	case errors.Is(err, webhook.ErrChannelBindingChanged):
		return &ConflictError{Msg: "channel binding changed; retry endpoint issuance"}
	case errors.Is(err, webhook.ErrInvalidChannelID),
		errors.Is(err, webhook.ErrInvalidOwnerUserID),
		errors.Is(err, webhook.ErrInvalidProvider),
		errors.Is(err, webhook.ErrOwnerInactive),
		errors.Is(err, webhook.ErrOwnerAgentForbidden),
		errors.Is(err, webhook.ErrAgentDisabled),
		errors.Is(err, webhook.ErrChannelNotWebhook):
		return invalid(err.Error())
	default:
		return err
	}
}
