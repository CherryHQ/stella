package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/channel"
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

// sendReplyOp replays a completed turn's recorded events through the card
// stream machinery: the progress card is created, updated through the
// recorded events, and finalized — one op identity, one terminal version.
// isGroup stays false; the reply anchor and thread come from the op address.
func (b *Bot) sendReplyOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.ReplyOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: bad send_reply payload: %v", err)
	}
	chatID := strings.TrimPrefix(op.Address.ChatKey, "feishu:")
	if chatID == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "feishu: empty chat key")
	}
	deliveryKey := op.DeliveryKey + ":" + fmt.Sprint(op.OperationIndex)
	sentMsgID, response, images, files, refs, elapsed, streamErr := b.streamResponseInThread(ctx, payload.ReplayStream().Events, chatID, op.Address.ReplyToKey, op.Address.ThreadKey, deliveryKey)
	if err := ctx.Err(); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "feishu: reply dispatch cancelled: %v", err)
	}
	if streamErr != nil {
		return channel.SendResult{}, classifyFeishuSend(streamErr)
	}
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	finalResponse := response + elapsedFooter(elapsed)
	if err := b.sendFinalResponseInThreadWithOptions(ctx, chatID, op.Address.ReplyToKey, op.Address.ThreadKey, sentMsgID, finalResponse, refs, false, false, cardStatusCompleted, deliveryKey); err != nil {
		return channel.SendResult{}, classifyFeishuSend(err)
	}
	for _, img := range images {
		if err := b.sendImageInThread(chatID, op.Address.ReplyToKey, op.Address.ThreadKey, img); err != nil {
			return channel.SendResult{}, classifyFeishuSend(err)
		}
	}
	for _, file := range files {
		if err := b.sendFileInThread(chatID, op.Address.ReplyToKey, op.Address.ThreadKey, file); err != nil {
			return channel.SendResult{}, classifyFeishuSend(err)
		}
	}
	return channel.SendResult{PlatformMessageID: sentMsgID}, nil
}
