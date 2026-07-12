package controlplane

import (
	"context"
	"encoding/json"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func channelFacts(ch config.Channel) policy.ChannelFacts {
	return policy.ChannelFacts{Kind: ch.Type, Status: providerStatus(ch.Enabled)}
}

// ListChannels returns every configured channel.
func (a *Access) ListChannels(ctx context.Context) ([]config.Channel, error) {
	if err := a.authorizeChannelList(); err != nil {
		return nil, err
	}
	return a.svc.store.ListChannels(ctx)
}

// GetChannel returns one channel by id (opaque 404 when missing).
func (a *Access) GetChannel(ctx context.Context, id string) (config.Channel, error) {
	if err := a.authorizeChannel(authz.ActionRead, id, policy.ChannelFacts{}); err != nil {
		return config.Channel{}, err
	}
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
	if err := a.authorizeChannel(authz.ActionManage, ch.ID, channelFacts(ch)); err != nil {
		return config.Channel{}, err
	}
	// POST is create-only: a silent upsert would let a re-POST overwrite an existing
	// channel's config and flip a deliberately disabled webhook back on.
	if create {
		if _, err := a.svc.store.GetChannel(ctx, ch.ID); err == nil {
			return config.Channel{}, &ConflictError{Msg: "channel already exists"}
		}
	}
	// A webhook is a runtime-less trigger: it must name the agent it runs, but its
	// caller is resolved dynamically from the PAT (not bound to one user).
	if ch.Type == pkgchannel.PlatformWebhook && ch.AgentID == "" {
		return config.Channel{}, invalid("webhook channel requires a bound agent")
	}
	if conflict, err := a.svc.channelAgentPlatformBindingConflict(ctx, ch); err != nil {
		return config.Channel{}, err
	} else if conflict != "" {
		return config.Channel{}, invalid(conflict)
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
	if err := a.svc.store.UpsertChannel(ctx, ch); err != nil {
		return config.Channel{}, err
	}
	if err := a.svc.ensureChannelPluginEnabled(ctx, ch.Type); err != nil {
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
	if err := a.authorizeChannel(authz.ActionManage, id, policy.ChannelFacts{}); err != nil {
		return err
	}
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
func (s *Service) ensureChannelPluginEnabled(ctx context.Context, channelType string) error {
	pluginID := config.PluginID(config.PluginKindChannel, channelType)
	return s.store.UpsertPlugin(ctx, config.Plugin{
		ID:      pluginID,
		Kind:    config.PluginKindChannel,
		Name:    channelType,
		Enabled: true,
		Config:  map[string]any{},
	})
}
