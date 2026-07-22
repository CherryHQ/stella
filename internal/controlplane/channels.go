package controlplane

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

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
	return a.svc.store.ListChannels(ctx)
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
	if err != nil {
		return config.Channel{}, notFound("channel not found")
	}
	return ch, nil
}

// SaveChannel validates and persists a channel, ensures its plugin is enabled,
// and applies its runtime. create=true rejects an id that already exists (the
// create-only POST contract). It returns the reloaded channel.
func (a *Access) SaveChannel(ctx context.Context, ch config.Channel, cfgMap map[string]any, create bool) (config.Channel, error) {
	operation, err := a.ManageChannel(ch.ID)
	if err != nil {
		return config.Channel{}, err
	}
	return operation.Save(ctx, ch, cfgMap, create)
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
	// Every channel mutation must be able to take the endpoint domain's shared
	// row lock. Do not silently fall back to an unlocked upsert: an old endpoint
	// could otherwise acquire a new Agent through a partial composition.
	if a.svc.webhooks == nil {
		return config.Channel{}, ErrUnavailable
	}
	// POST is create-only: a silent upsert would let a re-POST overwrite an existing
	// channel's config and flip a deliberately disabled webhook back on.
	if create {
		if _, err := a.svc.store.GetChannel(ctx, ch.ID); err == nil {
			return config.Channel{}, &ConflictError{Msg: "channel already exists"}
		}
	}
	// A webhook is a runtime-less trigger: it must name the Agent an issued
	// capability endpoint will run for its fixed owner.
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
	if !create {
		// Every existing-channel write takes the endpoint domain's channel-row
		// lock. It permits safe behavior/allowlist changes but rejects a type,
		// agent, or provider change while an endpoint is active.
		if _, err := a.svc.store.GetChannel(ctx, ch.ID); err == nil {
			err := a.svc.webhooks.UpdateChannel(ctx, webhook.ChannelBinding{
				ChannelID: ch.ID,
				AgentID:   ch.AgentID,
			}, ch.Name, ch.Type, ch.Enabled, ch.Config)
			if err != nil {
				return config.Channel{}, endpointError(err)
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			if err := a.svc.store.UpsertChannel(ctx, ch); err != nil {
				return config.Channel{}, channelSaveError(err)
			}
		} else {
			return config.Channel{}, err
		}
	} else if err := a.svc.store.UpsertChannel(ctx, ch); err != nil {
		return config.Channel{}, channelSaveError(err)
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

// DeleteChannel stops a channel's runtime and removes it.
func (a *Access) DeleteChannel(ctx context.Context, id string) error {
	ch, err := a.svc.store.GetChannel(ctx, id)
	if err != nil {
		return notFound("channel not found")
	}
	ch.Enabled = false
	if err := a.svc.plugins.ApplyChannel(ctx, ch); err != nil {
		a.svc.log.Error("failed to stop channel runtime", "channel_id", ch.ID, "channel_type", ch.Type, "error", err)
	}
	return a.svc.store.DeleteChannel(ctx, id)
}

// channelAgentPlatformBindingConflict enforces one bidirectional-channel binding
// per (agent, platform). Webhooks are exempt: an agent may back many endpoints.
// A non-empty string is the client-facing conflict message.
//
// This mirrors the server-side helper that the feishu/weixin registration
// handlers still use; the admin channel CRUD path owns its own copy so those
// out-of-scope ingress handlers are untouched.
func channelSaveError(err error) error {
	var conflict *config.ChannelBindingConflictError
	if errors.As(err, &conflict) {
		return invalid(conflict.Error())
	}
	return err
}

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
