package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

// SendOperation implements channel.OperationSender: one durable outbox op maps
// to one Discord REST call. ChatKey is the Discord channel id (guild or DM),
// ReplyToKey the platform message id to soft-reference.
func (b *Bot) SendOperation(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	if op.Kind == "send_group_reply" {
		var payload channel.GroupReplyOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: bad send_group_reply payload: %v", err)
		}
		if err := b.Publish(ctx, payload.PublishRequest()); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "discord: group reply publish: %v", err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind == "notify" {
		var payload struct {
			Notification channel.Notification `json:"notification"`
		}
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: bad notify payload: %v", err)
		}
		if err := b.Notify(ctx, payload.Notification); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "discord: notify send: %v", err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind == "send_reply" {
		var payload channel.ReplyOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: bad send_reply payload: %v", err)
		}
		if op.Address.ChatKey == "" {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: empty chat key")
		}
		// Replay the recorded turn through the draft/edit path: the draft is
		// created, ticked through the recorded progress, and finalized — one
		// op identity, one terminal version. No cancel control: the run has
		// already finished when the op dispatches.
		if err := b.deliverReplay(ctx, op.Address.ChatKey, op.Address.ReplyToKey, payload.ReplayStream(), nil, true); err != nil {
			return channel.SendResult{}, classifyDiscordSend(err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind != "send_text" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: unsupported op kind %q", op.Kind)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: bad send_text payload: %v", err)
	}
	if op.Address.ChatKey == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: empty chat key")
	}
	if b.rest == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "discord: REST client unavailable")
	}
	msg := &discordgo.MessageSend{Content: payload.Text, AllowedMentions: noMentions()}
	msg.Reference = softReference(op.Address.ChatKey, op.Address.ReplyToKey)
	sent, err := b.rest.ChannelMessageSendComplex(op.Address.ChatKey, msg, discordgo.WithContext(ctx))
	if err != nil {
		return channel.SendResult{}, classifyDiscordSend(err)
	}
	id := ""
	if sent != nil {
		id = sent.ID
	}
	return channel.SendResult{PlatformMessageID: id}, nil
}

// classifyDiscordSend maps a discordgo failure to a ledger class. A real API
// response decides; transport failures after the request are unknown.
func classifyDiscordSend(err error) error {
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr.Response != nil {
		switch code := restErr.Response.StatusCode; {
		case code == 429 || code >= 500:
			return &channel.SendError{Class: channel.SendRetryable, Err: err}
		default:
			return &channel.SendError{Class: channel.SendPermanent, Err: err}
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return &channel.SendError{Class: channel.SendRetryable, Err: err}
		}
		return &channel.SendError{Class: channel.SendUnknown, Err: err}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &channel.SendError{Class: channel.SendUnknown, Err: err}
	}
	return &channel.SendError{Class: channel.SendUnknown, Err: err}
}

// OwnsAccount checks the bot identity captured at receive time.
func (b *Bot) OwnsAccount(accountKey string) bool {
	return b.botID != "" && b.botID == accountKey
}
