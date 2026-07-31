package controlplane

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/webhook"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// ChannelManagement is one channel-management operation. Its Save method owns the
// channel row plus the required channel plugin enable/apply as a single unit, so
// callers never need a second plugin call.
type ChannelManagement struct{ access *Access }

// ManageChannel opens a channel-management operation. The admin gate already ran
// at Begin, so this only hands back the operation handle; id is retained for
// call-site symmetry (registration flows keep this handle across their platform
// handshake, then call Save after credentials arrive).
func (a *Access) ManageChannel(id string) (*ChannelManagement, error) {
	_ = id
	return &ChannelManagement{access: a}, nil
}

// ListChannels returns every configured channel.
// ValidateBinding checks the durable channel-binding invariant before an
// enrollment flow performs an external handshake. Save repeats the check and the
// database unique index closes the concurrent-write race.
func (m *ChannelManagement) ValidateBinding(ctx context.Context, ch config.Channel) error {
	conflict, err := m.access.svc.channelAgentPlatformBindingConflict(ctx, ch)
	if err != nil {
		return err
	}
	if conflict != "" {
		return invalid(conflict)
	}
	return nil
}

func (a *Access) ListChannels(ctx context.Context) ([]config.Channel, error) {
	channels, err := a.svc.store.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]config.Channel, 0, len(channels))
	for _, ch := range channels {
		if a.canReadChannel(ch) {
			visible = append(visible, ch)
		}
	}
	return visible, nil
}

// LookupAgent reads an agent for a channel-binding precondition: a registration
// flow binds a channel to an agent that must exist and be enabled. It is an
// admin-gated read (the Access already passed Begin), so a non-admin never
// reaches it; the caller inspects the returned agent's Enabled flag.
func (a *Access) LookupAgent(ctx context.Context, id string) (config.Agent, error) {
	return a.svc.store.GetAgent(ctx, id)
}

// GetChannel returns one channel by id (opaque 404 when missing).
func (a *Access) GetChannel(ctx context.Context, id string) (config.Channel, error) {
	ch, err := a.svc.store.GetChannel(ctx, id)
	if err != nil || !a.canReadChannel(ch) {
		return config.Channel{}, notFound("channel not found")
	}
	return ch, nil
}

func (a *Access) canReadChannel(ch config.Channel) bool {
	if effectiveChannelType(ch) == pkgchannel.PlatformWebhook {
		return ch.OwnerUserID == string(a.authority.UserID()) || (a.authority.IsAdmin() && ch.OwnerUserID == "")
	}
	return a.authority.IsAdmin()
}

func (a *Access) requireChannelWrite(ch config.Channel) error {
	if effectiveChannelType(ch) == pkgchannel.PlatformWebhook {
		if ch.OwnerUserID != string(a.authority.UserID()) {
			return notFound("channel not found")
		}
		return nil
	}
	if !a.authority.IsAdmin() {
		return authz.ErrForbidden
	}
	return nil
}

// SaveChannel validates and persists a channel, ensures its plugin is enabled,
// and applies its runtime. create=true rejects an id that already exists (the
// create-only POST contract). It returns the reloaded channel.
func (a *Access) SaveChannel(ctx context.Context, ch config.Channel, cfgMap map[string]any, create bool) (config.Channel, error) {
	ownerID := string(a.authority.UserID())
	channelType := effectiveChannelType(ch)

	if !create {
		if channelType != pkgchannel.PlatformWebhook && !a.authority.IsAdmin() {
			return config.Channel{}, authz.ErrForbidden
		}
		existing, err := a.svc.store.GetChannel(ctx, ch.ID)
		if err != nil {
			// PATCH is update-only. Besides matching the API contract, refusing
			// create-on-miss prevents a concurrent personal webhook with the same ID
			// from being overwritten by an admin deployment-channel upsert.
			return config.Channel{}, notFound("channel not found")
		}
		if !a.canReadChannel(existing) {
			return config.Channel{}, notFound("channel not found")
		}
		currentType := effectiveChannelType(existing)
		if channelType != currentType {
			// Personal webhooks and deployment channels have different ownership
			// models. Converting between them would silently change authority.
			return config.Channel{}, invalid("channel type cannot be changed")
		}
		ch.OwnerUserID = existing.OwnerUserID
		if currentType == pkgchannel.PlatformWebhook {
			if err := a.requireChannelWrite(existing); err != nil {
				return config.Channel{}, err
			}
			if existing.AgentID != ch.AgentID {
				if err := a.requirePersonalWebhookAgent(ctx, ownerID, ch.AgentID); err != nil {
					return config.Channel{}, err
				}
			}
			return a.savePersonalWebhook(ctx, ch, cfgMap, false)
		}
		if !a.authority.IsAdmin() {
			return config.Channel{}, authz.ErrForbidden
		}
		operation, err := a.ManageChannel(ch.ID)
		if err != nil {
			return config.Channel{}, err
		}
		return operation.Save(ctx, ch, cfgMap, false)
	}

	if channelType == pkgchannel.PlatformWebhook {
		ch.OwnerUserID = ownerID
		if err := a.requirePersonalWebhookAgent(ctx, ownerID, ch.AgentID); err != nil {
			return config.Channel{}, err
		}
		return a.savePersonalWebhook(ctx, ch, cfgMap, true)
	}
	if !a.authority.IsAdmin() {
		return config.Channel{}, authz.ErrForbidden
	}
	operation, err := a.ManageChannel(ch.ID)
	if err != nil {
		return config.Channel{}, err
	}
	return operation.Save(ctx, ch, cfgMap, true)
}

func (a *Access) requirePersonalWebhookAgent(ctx context.Context, ownerID, agentID string) error {
	if agentID == "" {
		return invalid("webhook channel requires a bound agent")
	}
	if a.svc.webhooks == nil {
		return unavailable("webhook endpoint service unavailable")
	}
	return endpointError(a.svc.webhooks.ValidateOwnerAgent(ctx, ownerID, agentID))
}

func (a *Access) savePersonalWebhook(ctx context.Context, ch config.Channel, cfgMap map[string]any, create bool) (config.Channel, error) {
	if a.svc.webhooks == nil {
		return config.Channel{}, unavailable("webhook endpoint service unavailable")
	}
	operation, err := a.ManageChannel(ch.ID)
	if err != nil {
		return config.Channel{}, err
	}
	// Store mutation shares the capability lifecycle fence. In particular, an
	// Admit that passed final validation cannot race a disable/rebind/delete.
	var saved config.Channel
	err = a.svc.webhooks.MutateChannel(func() error {
		saved, err = operation.Save(ctx, ch, cfgMap, create)
		return err
	})
	return saved, err
}

// Channel reads the current row for this operation's channel so a caller can
// merge a partial update onto it. It is an admin-gated read (Begin already ran);
// a missing channel returns an error the caller treats as "start from a fresh
// row" rather than a hard failure.
func (m *ChannelManagement) Channel(ctx context.Context, id string) (config.Channel, error) {
	return m.access.svc.store.GetChannel(ctx, id)
}

// Save persists the already-authorized channel and applies its plugin as one
// control-plane operation. It intentionally makes no further authorization call.
func (m *ChannelManagement) Save(ctx context.Context, ch config.Channel, cfgMap map[string]any, create bool) (config.Channel, error) {
	a := m.access
	// POST is create-only: a silent upsert would let a re-POST overwrite an existing
	// channel's config and flip a deliberately disabled webhook back on.
	if create {
		if _, err := a.svc.store.GetChannel(ctx, ch.ID); err == nil {
			return config.Channel{}, &ConflictError{Msg: "channel already exists"}
		}
	}
	// A webhook is a runtime-less personal trigger and must name its Agent. Its
	// owner is persisted on the channel and never accepted from request input.
	if ch.Type == pkgchannel.PlatformWebhook && ch.AgentID == "" {
		return config.Channel{}, invalid("webhook channel requires a bound agent")
	}
	if err := m.ValidateBinding(ctx, ch); err != nil {
		return config.Channel{}, err
	}
	pluginID := config.PluginID(config.PluginKindChannel, ch.Type)
	if err := a.svc.plugins.ValidateConfig(pluginID, cfgMap); err != nil {
		return config.Channel{}, invalid("invalid request")
	}
	cfgJSON, err := json.Marshal(cfgMap)
	if err != nil {
		return config.Channel{}, invalid("invalid config JSON")
	}
	ch.Config = string(cfgJSON)
	if create {
		if err := a.svc.store.UpsertChannel(ctx, ch); err != nil {
			return config.Channel{}, channelSaveError(err)
		}
	} else {
		// UpdateChannel takes the exact channel row lock endpoint issuance holds.
		// It permits behavior changes but rejects a binding change against an
		// active endpoint. PATCH never creates an absent resource.
		if err := a.svc.store.UpdateChannel(ctx, ch); err != nil {
			return config.Channel{}, channelSaveError(err)
		}
	}
	if err := a.svc.ensureChannelPluginEnabled(ctx, ch.Type, cfgMap); err != nil {
		return config.Channel{}, err
	}
	if err := a.svc.plugins.ApplyChannel(ctx, ch); err != nil {
		a.svc.log.Error("failed to apply channel runtime", "channel_id", ch.ID, "channel_type", ch.Type, "error", err)
	}
	saved, err := a.svc.store.GetChannel(ctx, ch.ID)
	if err != nil {
		return config.Channel{}, err
	}
	return saved, nil
}

// DeleteChannel stops a channel's runtime and removes it. A webhook channel with
// an active capability endpoint is rejected before any side effect: the endpoint
// must be revoked first. The store's RESTRICT foreign key is the race-safe
// backstop should an endpoint be issued between this check and the delete.
func (a *Access) DeleteChannel(ctx context.Context, id string) error {
	ch, err := a.svc.store.GetChannel(ctx, id)
	if err != nil {
		return notFound("channel not found")
	}
	legacyWebhookCleanup := effectiveChannelType(ch) == pkgchannel.PlatformWebhook && ch.OwnerUserID == "" && a.authority.IsAdmin()
	if !legacyWebhookCleanup {
		if err := a.requireChannelWrite(ch); err != nil {
			return err
		}
	}
	if effectiveChannelType(ch) == pkgchannel.PlatformWebhook && a.svc.webhooks == nil {
		return unavailable("webhook endpoint service unavailable")
	}
	deleteFn := func() error {
		if effectiveChannelType(ch) == pkgchannel.PlatformWebhook && a.svc.webhooks != nil {
			if _, err := a.svc.webhooks.GetByChannel(ctx, id, ch.OwnerUserID); err == nil {
				return &ConflictError{Msg: "webhook endpoint is active; revoke it before deleting the channel"}
			} else if !errors.Is(err, webhook.ErrNotFound) {
				return err
			}
		}
		ch.Enabled = false
		if err := a.svc.plugins.ApplyChannel(ctx, ch); err != nil {
			a.svc.log.Error("failed to stop channel runtime", "channel_id", ch.ID, "channel_type", ch.Type, "error", err)
		}
		if err := a.svc.store.DeleteChannel(ctx, id); err != nil {
			if errors.Is(err, config.ErrChannelEndpointActive) {
				return &ConflictError{Msg: "webhook endpoint is active; revoke it before deleting the channel"}
			}
			return err
		}
		return nil
	}
	if effectiveChannelType(ch) == pkgchannel.PlatformWebhook {
		return a.svc.webhooks.MutateChannel(deleteFn)
	}
	return deleteFn()
}

func channelSaveError(err error) error {
	var conflict *config.ChannelBindingConflictError
	switch {
	case errors.Is(err, config.ErrChannelEndpointActive):
		return &ConflictError{Msg: "webhook endpoint is active; revoke it before changing the channel binding"}
	case errors.As(err, &conflict):
		return invalid(conflict.Error())
	}
	return err
}

// channelAgentPlatformBindingConflict enforces one bidirectional-channel binding
// per (agent, platform). Webhooks are exempt: an agent may back many endpoints.
// A non-empty string is the client-facing conflict message.
//
// This mirrors the server-side helper that the feishu/weixin registration
// handlers still use; the admin channel CRUD path owns its own copy so those
// out-of-scope ingress handlers are untouched.
func (s *Service) channelAgentPlatformBindingConflict(ctx context.Context, ch config.Channel) (string, error) {
	if ch.AgentID == "" || ch.Type == "" {
		return "", nil
	}
	if ch.Type == pkgchannel.PlatformWebhook {
		return "", nil
	}
	channels, err := s.store.ListChannels(ctx)
	if err != nil {
		return "", err
	}
	for _, existing := range channels {
		if existing.ID == ch.ID {
			continue
		}
		existingType := existing.Type
		if existingType == "" {
			existingType = existing.ID
		}
		if existingType == ch.Type && existing.AgentID == ch.AgentID {
			return "agent is already bound to " + ch.Type + " channel " + existing.ID, nil
		}
	}
	return "", nil
}

// ensureChannelPluginEnabled upserts the channel-kind plugin row as enabled so a
// newly saved channel's runtime is registered.
func (s *Service) ensureChannelPluginEnabled(ctx context.Context, channelType string, cfg map[string]any) error {
	pluginID := config.PluginID(config.PluginKindChannel, channelType)
	pluginConfig := map[string]any{}
	// Weixin's status surface reads its singleton credentials from the channel
	// plugin row. Other channel instances keep credentials only on their rows.
	if channelType == pkgchannel.PlatformWeixin {
		pluginConfig = cfg
	}
	return s.store.UpsertPlugin(ctx, config.Plugin{
		ID:      pluginID,
		Kind:    config.PluginKindChannel,
		Name:    channelType,
		Enabled: true,
		Config:  pluginConfig,
	})
}
