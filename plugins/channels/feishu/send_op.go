package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

// SendOperation implements channel.OperationSender: one durable outbox op maps
// to one Feishu message. Feishu accepts a client-supplied uuid for idempotent
// create — the op's stable key is sent verbatim so a replayed attempt after a
// lost response cannot produce a second message.
func (b *Bot) SendOperation(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	if op.Kind == "send_group_reply" {
		var payload channel.GroupReplyOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: bad send_group_reply payload: %v", err)
		}
		if err := b.Publish(ctx, payload.PublishRequest()); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "feishu: group reply publish: %v", err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind == "notify" {
		var payload struct {
			Notification channel.Notification `json:"notification"`
		}
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: bad notify payload: %v", err)
		}
		if err := b.Notify(ctx, payload.Notification); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "feishu: notify send: %v", err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind == "send_reply" {
		return b.sendReplyOp(ctx, op)
	}
	if op.Kind == "send_attachment" {
		return b.sendAttachmentOp(ctx, op)
	}
	if op.Kind == "draft_update" {
		return b.SendDraftUpdate(ctx, op)
	}
	if op.Kind != "send_text" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: unsupported op kind %q", op.Kind)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: bad send_text payload: %v", err)
	}
	if op.Address.ChatKey == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: empty chat key")
	}
	// Feishu's create uuid is any <=50-char unique string; hashing keeps the
	// delivery key inside that budget.
	idem := uuid.NewSHA1(uuid.NameSpaceURL, []byte(op.DeliveryKey+":"+fmt.Sprint(op.OperationIndex))).String()

	var messageID string
	var err error
	content, buildErr := buildCardContentForStatus(payload.Text, cardStatusCompleted)
	switch {
	case buildErr != nil && op.Address.ReplyToKey != "":
		// Card build failure is a payload problem: degrade to plain text.
		err = b.replyText(ctx, op.Address.ReplyToKey, payload.Text, false)
	case buildErr != nil:
		err = b.sendTextToChat(ctx, op.Address.ChatKey, payload.Text)
	case op.Address.ReplyToKey != "":
		messageID, err = b.sendBuiltCardReply(ctx, op.Address.ReplyToKey, content, false, idem)
	default:
		messageID, err = b.sendBuiltCardToChat(ctx, op.Address.ChatKey, content, idem)
	}
	if err != nil {
		return channel.SendResult{}, classifyFeishuSend(err)
	}
	return channel.SendResult{PlatformMessageID: messageID}, nil
}

// classifyFeishuSend maps the existing delivery-outcome machinery to the
// ledger classes: not_sent is safe to retry, unknown holds for a probe, and
// API rejections that are not transient are permanent.
func classifyFeishuSend(err error) error {
	var delivery *feishuDeliveryError
	if errors.As(err, &delivery) {
		if delivery.outcome == deliveryUnknown {
			return &channel.SendError{Class: channel.SendUnknown, Err: err}
		}
		// not_sent: distinguish a transient rejection from a real 4xx.
		var apiErr *feishuAPIError
		if errors.As(err, &apiErr) && !isTransientFeishuError(apiErr) {
			return &channel.SendError{Class: channel.SendPermanent, Err: err}
		}
		return &channel.SendError{Class: channel.SendRetryable, Err: err}
	}
	return &channel.SendError{Class: channel.SendUnknown, Err: err}
}

// OwnsAccount checks the bot identity captured at receive time.
func (b *Bot) OwnsAccount(accountKey string) bool {
	return b.registeredBotID != "" && b.registeredBotID == accountKey
}

// SendDraftUpdate implements channel.DraftSender: one card patched in place
// while the run executes. The reply op finalizes the same card.
func (b *Bot) SendDraftUpdate(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.DraftUpdatePayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: bad draft payload: %v", err)
	}
	chatID := strings.TrimPrefix(op.Address.ChatKey, "feishu:")
	if chatID == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: empty chat key")
	}
	text := payload.Text
	if strings.TrimSpace(text) == "" {
		text = "…"
	}
	if op.DraftMessageID != "" {
		if err := b.patchMessageForStatus(ctx, op.DraftMessageID, text, cardStatusRunning); err != nil {
			return channel.SendResult{}, classifyFeishuSend(err)
		}
		return channel.SendResult{PlatformMessageID: op.DraftMessageID}, nil
	}
	idem := uuid.NewSHA1(uuid.NameSpaceURL, []byte(op.DeliveryKey+":"+fmt.Sprint(op.OperationIndex))).String()
	messageID, err := b.deliverCardWithOptions(ctx, chatID, op.Address.ReplyToKey, text, false, cardStatusRunning, idem)
	if err != nil {
		return channel.SendResult{}, classifyFeishuSend(err)
	}
	return channel.SendResult{PlatformMessageID: messageID}, nil
}

// sendReplyOp makes exactly one platform call: finalize the live draft card
// when a run produced one, else create the terminal card directly. The
// create carries the delivery-key uuid, so a retried op converges to the
// same message instead of duplicating. Attachments are sibling ops.
func (b *Bot) sendReplyOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.ReplyOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: bad send_reply payload: %v", err)
	}
	chatID := strings.TrimPrefix(op.Address.ChatKey, "feishu:")
	if chatID == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: empty chat key")
	}
	var sb strings.Builder
	var refs []renderrefs.Reference
	for _, evt := range payload.Events {
		refs = append(refs, evt.References...)
		sb.WriteString(evt.Text)
	}
	response := sb.String()
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	deliveryKey := op.DeliveryKey + ":" + fmt.Sprint(op.OperationIndex)
	// The finalize helper may issue more than one call (patch + overflow
	// chunks); re-validate ownership before the external calls it makes.
	if err := op.CheckOwnership(ctx); err != nil {
		return channel.SendResult{}, err
	}
	if op.DraftMessageID != "" {
		if err := b.sendFinalResponseInThreadWithOptions(ctx, chatID, op.Address.ReplyToKey, op.Address.ThreadKey, op.DraftMessageID, response, dedupeReferences(refs), false, false, cardStatusCompleted, deliveryKey); err != nil {
			return channel.SendResult{}, classifyFeishuSend(err)
		}
		return channel.SendResult{PlatformMessageID: op.DraftMessageID}, nil
	}
	messageID, err := b.deliverCardWithOptions(ctx, chatID, op.Address.ReplyToKey, response, false, cardStatusCompleted, uuid.NewSHA1(uuid.NameSpaceURL, []byte(deliveryKey)).String())
	if err != nil {
		return channel.SendResult{}, classifyFeishuSend(err)
	}
	return channel.SendResult{PlatformMessageID: messageID}, nil
}

// sendAttachmentOp delivers one image or file into the reply thread — one
// durable op, one platform call, one receipt row.
func (b *Bot) sendAttachmentOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.AttachmentOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: bad send_attachment payload: %v", err)
	}
	chatID := strings.TrimPrefix(op.Address.ChatKey, "feishu:")
	if chatID == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: empty chat key")
	}
	var err error
	switch payload.Kind {
	case channel.AttachmentImage:
		err = b.sendImageInThread(chatID, op.Address.ReplyToKey, op.Address.ThreadKey, channel.ImageEvent{Data: payload.Data, MimeType: payload.MimeType})
	case channel.AttachmentFile:
		err = b.sendFileInThread(chatID, op.Address.ReplyToKey, op.Address.ThreadKey, channel.FileEvent{Path: payload.Path, Name: payload.Name})
	default:
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: unknown attachment kind %q", payload.Kind)
	}
	if err != nil {
		return channel.SendResult{}, classifyFeishuSend(err)
	}
	return channel.SendResult{}, nil
}
