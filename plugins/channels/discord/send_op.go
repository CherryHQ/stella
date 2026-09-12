package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

// SendOperation implements channel.OperationSender: one durable outbox op maps
// to one Discord REST call. ChatKey is the Discord channel id (guild or DM),
// ReplyToKey the platform message id to soft-reference.
func (b *Bot) SendOperation(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	if op.Kind == "send_group_reply" {
		return b.sendGroupReplyOp(ctx, op)
	}
	if op.Kind == "notify" {
		return b.sendNotifyOp(ctx, op)
	}
	if op.Kind == "send_reply" {
		var payload channel.ReplyOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: bad send_reply payload: %v", err)
		}
		if op.Address.ChatKey == "" {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: empty chat key")
		}
		if b.rest == nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendRetryable, "discord: REST client unavailable")
		}
		// One platform call per op: edit the live draft to its final content,
		// or post the primary segment as a new message. Overflow text and
		// attachments are sibling ops with their own receipts.
		text := payload.Text
		if text == "" {
			text = "(empty response)"
		}
		if op.DraftMessageID != "" {
			edit := discordgo.NewMessageEdit(op.Address.ChatKey, op.DraftMessageID).SetContent(text)
			edit.AllowedMentions = noMentions()
			if _, err := b.rest.ChannelMessageEditComplex(edit, discordgo.WithContext(ctx)); err != nil {
				return channel.SendResult{}, classifyDiscordSend(err)
			}
			return channel.SendResult{PlatformMessageID: op.DraftMessageID}, nil
		}
		msg := &discordgo.MessageSend{Content: text, AllowedMentions: noMentions()}
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
	if op.Kind == "send_attachment" {
		var payload channel.AttachmentOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: bad send_attachment payload: %v", err)
		}
		var err error
		target := opTargetChannel(op)
		switch payload.Kind {
		case channel.AttachmentImage:
			err = b.sendImage(ctx, target, channel.ImageEvent{Data: payload.Data, MimeType: payload.MimeType})
		case channel.AttachmentFile:
			data, oerr := channel.OpenAttachmentOp(ctx, b.handler, op)
			if oerr != nil {
				err = channel.ClassifyAttachmentErr("discord", oerr)
				break
			}
			name := payload.Name
			if name == "" {
				name = "file"
			}
			err = b.sendFileData(ctx, target, name, bytes.NewReader(data))
		default:
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: unknown attachment kind %q", payload.Kind)
		}
		if err != nil {
			return channel.SendResult{}, classifyDiscordSend(err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind == "draft_update" {
		return b.SendDraftUpdate(ctx, op)
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
	target := opTargetChannel(op)
	if target == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: empty chat key")
	}
	if b.rest == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "discord: REST client unavailable")
	}
	msg := &discordgo.MessageSend{Content: payload.Text, AllowedMentions: noMentions()}
	msg.Reference = softReference(target, op.Address.ReplyToKey)
	sent, err := b.rest.ChannelMessageSendComplex(target, msg, discordgo.WithContext(ctx))
	if err != nil {
		return channel.SendResult{}, classifyDiscordSend(err)
	}
	id := ""
	if sent != nil {
		id = sent.ID
	}
	return channel.SendResult{PlatformMessageID: id}, nil
}

// opTargetChannel picks the Discord channel an op posts to: a group op's
// thread id when set (threads are channels in Discord), else the chat key.
func opTargetChannel(op channel.OutboundOp) string {
	if op.Address.Scope == "group" && op.Address.ThreadKey != "" {
		return op.Address.ThreadKey
	}
	return op.Address.ChatKey
}

// sendGroupReplyOp delivers the primary segment of an accepted group reply —
// exactly one REST call — with the lifecycle reaction pair around it when the
// turn opted into feedback. Overflow and attachments are sibling ops.
func (b *Bot) sendGroupReplyOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.GroupReplyOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: bad send_group_reply payload: %v", err)
	}
	target := payload.PlatformThreadID
	if target == "" {
		target = payload.PlatformGroupID
	}
	if target == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: empty group target")
	}
	if b.rest == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "discord: REST client unavailable")
	}
	text := payload.Text
	if text == "" {
		text = "(empty response)"
	}
	msg := &discordgo.MessageSend{Content: text, AllowedMentions: noMentions()}
	msg.Reference = softReference(target, payload.ReplyTo)
	sent, err := b.rest.ChannelMessageSendComplex(target, msg, discordgo.WithContext(ctx))
	if payload.LifecycleFeedback {
		if err == nil {
			b.finishReaction(context.WithoutCancel(ctx), target, payload.ReplyTo, true)
		} else {
			b.clearReactionLifecycle(context.WithoutCancel(ctx), target, payload.ReplyTo)
		}
	}
	if err != nil {
		return channel.SendResult{}, classifyDiscordSend(err)
	}
	id := ""
	if sent != nil {
		id = sent.ID
	}
	return channel.SendResult{PlatformMessageID: id}, nil
}

// sendNotifyOp delivers one notification segment — one REST call, one
// receipt. RecipientID opens the DM channel first; the lease guard runs
// before the send so a fenced-out owner stops at the boundary.
func (b *Bot) sendNotifyOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload struct {
		Notification channel.Notification `json:"notification"`
	}
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: bad notify payload: %v", err)
	}
	n := payload.Notification
	if n.RecipientID != "" {
		dm, err := b.session.UserChannelCreate(n.RecipientID)
		if err != nil {
			return channel.SendResult{}, classifyDiscordSend(fmt.Errorf("discord: create recipient DM: %w", err))
		}
		if dm == nil || dm.ID == "" {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: recipient DM has no channel ID")
		}
		n.ChatID = dm.ID
	}
	if n.ChatID == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: no target chat ID")
	}
	if b.rest == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "discord: REST client unavailable")
	}
	if err := op.CheckOwnership(ctx); err != nil {
		return channel.SendResult{}, err
	}
	msg := &discordgo.MessageSend{Content: n.Text, AllowedMentions: noMentions()}
	if n.Silent {
		msg.Flags = discordgo.MessageFlagsSuppressNotifications
	}
	sent, err := b.rest.ChannelMessageSendComplex(n.ChatID, msg, discordgo.WithContext(ctx))
	if err != nil {
		return channel.SendResult{}, classifyDiscordSend(err)
	}
	id := ""
	if sent != nil {
		id = sent.ID
	}
	return channel.SendResult{PlatformMessageID: id}, nil
}

// SendDraftUpdate implements channel.DraftSender: one message edited in
// place while the run executes — the same draft/edit pair deliverStream
// uses, minus the Cancel control (the outbox carries no requester binding).
func (b *Bot) SendDraftUpdate(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	if b.rest == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendRetryable, "discord: REST client unavailable")
	}
	var payload channel.DraftUpdatePayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: bad draft payload: %v", err)
	}
	if op.Address.ChatKey == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "discord: empty chat key")
	}
	display := buildDraftDisplay(payload.Text, &channel.ToolTracker{})
	if op.DraftMessageID != "" {
		edit := discordgo.NewMessageEdit(op.Address.ChatKey, op.DraftMessageID).SetContent(display)
		edit.AllowedMentions = noMentions()
		if _, err := b.rest.ChannelMessageEditComplex(edit, discordgo.WithContext(ctx)); err != nil {
			return channel.SendResult{}, classifyDiscordSend(err)
		}
		return channel.SendResult{PlatformMessageID: op.DraftMessageID}, nil
	}
	sent, err := b.rest.ChannelMessageSendComplex(op.Address.ChatKey, &discordgo.MessageSend{
		Content:         display,
		AllowedMentions: noMentions(),
		Reference:       softReference(op.Address.ChatKey, op.Address.ReplyToKey),
	}, discordgo.WithContext(ctx))
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
	var se *channel.SendError
	if errors.As(err, &se) {
		return se
	}
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
