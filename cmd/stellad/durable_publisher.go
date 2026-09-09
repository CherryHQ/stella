package main

import (
	"context"
	"encoding/json"
	"fmt"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/platform/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/plugins/channels/dingtalk"
	"github.com/CherryHQ/stella/plugins/channels/discord"
	"github.com/CherryHQ/stella/plugins/channels/feishu"
	"github.com/CherryHQ/stella/plugins/channels/qq"
	"github.com/CherryHQ/stella/plugins/channels/telegram"
	"github.com/CherryHQ/stella/plugins/channels/weixin"
)

// durablePublisherReconstructor is deliberately composition-root code: the
// channel domain owns the interface while only stellad may import every
// concrete plugin. Each result is a fresh egress-only client and therefore
// works on a non-leader replica with no listener registration.
type durablePublisherReconstructor struct {
	capabilities internalchannel.DurableReplyCapabilityResolver
}

func (r *durablePublisherReconstructor) ReconstructIncomingPublisher(ctx context.Context, ch config.Channel, envelope internalchannel.GroupOutboxEnvelope) (pkgchannel.DurablePublisher, error) {
	if !ch.Enabled {
		return nil, fmt.Errorf("channel %q is disabled", ch.ID)
	}
	if ch.Type == pkgchannel.PlatformWeixin {
		if r.capabilities == nil || envelope.ReplyCapabilityRef == "" {
			return nil, fmt.Errorf("weixin durable reply capability is unavailable")
		}
		capability, err := r.capabilities.Resolve(ctx, envelope.ReplyCapabilityRef, ch.ID, "weixin_context_token")
		if err != nil {
			return nil, fmt.Errorf("resolve Weixin reply capability: %w", err)
		}
		var cfg weixin.Config
		if err := json.Unmarshal([]byte(ch.Config), &cfg); err != nil {
			return nil, fmt.Errorf("decode Weixin publisher config: %w", err)
		}
		cfg.InstanceID = ch.ID
		return weixin.NewDurablePublisher(cfg, capability.Secret, capability.ExpiresAt)
	}
	group, err := r.ReconstructGroupPublisher(ctx, ch, envelope)
	if err != nil {
		return nil, err
	}
	publisher, ok := group.(pkgchannel.DurablePublisher)
	if !ok {
		return nil, fmt.Errorf("channel type %q has no durable incoming publisher", ch.Type)
	}
	return publisher, nil
}

func newDurablePublisherReconstructor(capabilities internalchannel.DurableReplyCapabilityResolver) *durablePublisherReconstructor {
	return &durablePublisherReconstructor{capabilities: capabilities}
}

func (r *durablePublisherReconstructor) ReconstructGroupPublisher(ctx context.Context, ch config.Channel, envelope internalchannel.GroupOutboxEnvelope) (pkgchannel.GroupPublisher, error) {
	if !ch.Enabled {
		return nil, fmt.Errorf("channel %q is disabled", ch.ID)
	}
	switch ch.Type {
	case "web":
		return internalchannel.NoopGroupPublisher(), nil
	case pkgchannel.PlatformTelegram:
		cfg, err := telegram.DecodeTelegramConfig([]byte(ch.Config))
		if err != nil {
			return nil, fmt.Errorf("decode Telegram publisher config: %w", err)
		}
		return telegram.NewDurableGroupPublisher(telegram.Config{
			InstanceID: ch.ID, Token: cfg.Token, ChannelID: cfg.ChannelID,
			AllowGroup: cfg.AllowGroup, AllowedChatIDs: cfg.AllowedChatIDs,
			AllowedTopicIDs: cfg.AllowedTopicIDs, AllowDM: cfg.AllowDM,
			RequireMention: cfg.RequireMention,
		})
	case pkgchannel.PlatformDiscord:
		cfg, err := discord.DecodeDiscordConfig([]byte(ch.Config))
		if err != nil {
			return nil, fmt.Errorf("decode Discord publisher config: %w", err)
		}
		return discord.NewDurableGroupPublisher(discord.Config{
			InstanceID: ch.ID, Token: cfg.Token, AllowGroup: cfg.AllowGroup,
			AllowAllGuilds: cfg.AllowAllGuilds, AllowedGuildIDs: cfg.AllowedGuildIDs,
			AllowedChannelIDs: cfg.AllowedChannelIDs, AllowedUserIDs: cfg.AllowedUserIDs,
			AllowedRoleIDs: cfg.AllowedRoleIDs, AllowDM: cfg.AllowDM,
			RequireMention: cfg.RequireMention,
		})
	case pkgchannel.PlatformQQ:
		var persisted qq.QQConfig
		if err := json.Unmarshal([]byte(ch.Config), &persisted); err != nil {
			return nil, fmt.Errorf("decode QQ publisher config: %w", err)
		}
		return qq.NewDurableGroupPublisher(qq.Config{
			InstanceID: ch.ID, AppID: persisted.AppID, AppSecret: persisted.AppSecret,
		})
	case pkgchannel.PlatformFeishu:
		cfg, err := feishu.DecodeFeishuConfig([]byte(ch.Config))
		if err != nil {
			return nil, fmt.Errorf("decode Feishu publisher config: %w", err)
		}
		groups := make(map[string]feishu.GroupConfig, len(cfg.Groups))
		for id, group := range cfg.Groups {
			groups[id] = feishu.GroupConfig{SystemPrompt: group.SystemPrompt, ToolAllow: group.ToolAllow, ToolDeny: group.ToolDeny}
		}
		return feishu.NewDurableGroupPublisher(feishu.Config{
			InstanceID: ch.ID, AppID: cfg.AppID, AppSecret: cfg.AppSecret,
			EncryptKey: cfg.EncryptKey, VerificationToken: cfg.VerificationToken,
			Groups: groups, TenantKey: cfg.TenantKey, AutoProvision: cfg.AutoProvision,
			AllowGroup: cfg.AllowGroup, AllowDM: cfg.AllowDM, RequireMention: cfg.RequireMention,
		})
	case pkgchannel.PlatformDingTalk:
		if envelope.ReplyCapabilityRef == "" || r.capabilities == nil {
			return nil, fmt.Errorf("DingTalk durable reply capability is unavailable")
		}
		capability, err := r.capabilities.Resolve(ctx, envelope.ReplyCapabilityRef, ch.ID, "dingtalk_session_webhook")
		if err != nil {
			return nil, fmt.Errorf("resolve DingTalk reply capability: %w", err)
		}
		return dingtalk.NewDurableGroupPublisher(capability.Secret, capability.ExpiresAt)
	case pkgchannel.PlatformWeixin:
		// Weixin currently has no group ingress or GroupPublisher contract. Do
		// not silently fall back to a listener-local context token if malformed
		// durable state nevertheless reaches this boundary.
		return nil, fmt.Errorf("weixin group publishing is not supported")
	default:
		return nil, fmt.Errorf("channel type %q has no durable group publisher", ch.Type)
	}
}
