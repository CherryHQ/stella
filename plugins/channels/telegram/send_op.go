package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/channel"
)

// SendOperation implements channel.OperationSender: one durable outbox
// operation maps to one Telegram call, with the result classified for the
// ledger — sent only on a real platform receipt, retryable when Telegram
// provably rejected it, unknown when the response never arrived.
func (b *Bot) SendOperation(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	if op.Kind == "send_group_reply" {
		return b.sendGroupReplyOp(ctx, op)
	}
	if op.Kind == "notify" {
		return b.sendNotifyOp(ctx, op)
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
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: unsupported op kind %q", op.Kind)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: bad send_text payload: %v", err)
	}
	chat := teleChatForKey(op.Address.ChatKey)
	if chat == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: empty chat key")
	}
	opts := &tele.SendOptions{ParseMode: tele.ModeMarkdownV2}
	if op.Address.ThreadKey != "" {
		if id, err := strconv.Atoi(op.Address.ThreadKey); err == nil {
			opts.ThreadID = id
		}
	}
	if op.Address.ReplyToKey != "" {
		if id, err := strconv.Atoi(op.Address.ReplyToKey); err == nil {
			opts.ReplyTo = &tele.Message{ID: id}
		}
	}

	msg, err := b.sendTelegramMarkdown(ctx, chat, payload.Text, opts, op.CheckOwnership)
	if err != nil {
		return channel.SendResult{}, classifySend(err)
	}
	return channel.SendResult{PlatformMessageID: strconv.Itoa(msg.ID)}, nil
}

// sendGroupReplyOp delivers the primary segment of an accepted group reply —
// exactly one platform call with the ack/terminal reaction pair around it.
// Overflow chunks and attachments are sibling ops with their own receipts.
func (b *Bot) sendGroupReplyOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.GroupReplyOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: bad send_group_reply payload: %v", err)
	}
	chatID, err := strconv.ParseInt(payload.PlatformGroupID, 10, 64)
	if err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: invalid group id %q: %v", payload.PlatformGroupID, err)
	}
	chat := &tele.Chat{ID: chatID}
	opts := &tele.SendOptions{ParseMode: tele.ModeMarkdownV2}
	if payload.PlatformThreadID != "" {
		if id, err := strconv.Atoi(payload.PlatformThreadID); err == nil {
			opts.ThreadID = id
		}
	}
	if payload.ReplyTo != "" {
		if replyID, err := strconv.Atoi(payload.ReplyTo); err == nil {
			opts.ReplyTo = &tele.Message{ID: replyID, Chat: chat}
			opts.AllowWithoutReply = true
		}
	}
	text := payload.Text
	if strings.TrimSpace(text) == "" {
		text = "(empty response)"
	}
	// Reactions are SDK calls and fall under the same ownership admission as
	// the send: a replica that lost the lease must not keep touching the
	// platform. They stay best-effort — a lost lease skips them and the send's
	// own check fails the op as retryable instead of a bare reaction error.
	// (Telegram reacts on every group reply — unlike Discord it does not gate
	// on LifecycleFeedback, matching the pre-outbox publisher behavior.)
	reacted := false
	if op.CheckOwnership(ctx) == nil {
		b.react(payload.PlatformGroupID, payload.ReplyTo, reactionReceived)
		reacted = true
	}
	var sendErr error
	defer func() {
		if reacted && op.CheckOwnership(context.WithoutCancel(ctx)) == nil {
			b.finishReaction(payload.PlatformGroupID, payload.ReplyTo, sendErr == nil)
		}
	}()
	var msg *tele.Message
	msg, sendErr = b.sendTelegramMarkdown(ctx, chat, text, opts, op.CheckOwnership)
	if sendErr != nil {
		return channel.SendResult{}, classifySend(sendErr)
	}
	return channel.SendResult{PlatformMessageID: strconv.Itoa(msg.ID)}, nil
}

// sendNotifyOp delivers one notification segment — one platform call, one
// receipt. Long notifications arrive pre-split as a chained op sequence.
func (b *Bot) sendNotifyOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload struct {
		Notification channel.Notification `json:"notification"`
	}
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: bad notify payload: %v", err)
	}
	chatID := payload.Notification.ChatID
	if chatID == "" {
		chatID = b.cfg.ChannelID
	}
	if chatID == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: no target chat ID")
	}
	chat := teleChatForKey(chatID)
	if chat == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: empty chat key")
	}
	opts := &tele.SendOptions{ParseMode: tele.ModeMarkdownV2, DisableNotification: payload.Notification.Silent}
	msg, err := b.sendTelegramMarkdown(ctx, chat, payload.Notification.Text, opts, op.CheckOwnership)
	if err != nil {
		return channel.SendResult{}, classifySend(err)
	}
	return channel.SendResult{PlatformMessageID: strconv.Itoa(msg.ID)}, nil
}

func teleChatForKey(key string) tele.Recipient {
	if numID, err := strconv.ParseInt(key, 10, 64); err == nil {
		return &tele.Chat{ID: numID}
	}
	if key == "" {
		return nil
	}
	return chatRef(key)
}

// classifySend maps a telebot failure to a ledger class. A real API response
// is decisive; a transport failure after the request may have still landed.
// An already-classified SendError (e.g. the ownership guard's retryable
// refusal) passes through unchanged.
func classifySend(err error) error {
	var se *channel.SendError
	if errors.As(err, &se) {
		return se
	}
	var flood tele.FloodError
	if errors.As(err, &flood) {
		return &channel.SendError{Class: channel.SendRetryable, Err: err}
	}
	var apiErr *tele.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code >= 500:
			return &channel.SendError{Class: channel.SendRetryable, Err: err}
		default:
			// 4xx and friends: the platform recorded and rejected the call.
			return &channel.SendError{Class: channel.SendPermanent, Err: err}
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if dnsErr, ok := unwrapDNSError(err); ok && dnsErr.IsNotFound {
			// DNS NXDOMAIN: nothing was sent.
			return &channel.SendError{Class: channel.SendRetryable, Err: err}
		}
		// Timeouts and post-write transport failures cannot prove the send
		// did not land.
		return &channel.SendError{Class: channel.SendUnknown, Err: err}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &channel.SendError{Class: channel.SendUnknown, Err: err}
	}
	return &channel.SendError{Class: channel.SendUnknown, Err: fmt.Errorf("unclassified send failure: %w", err)}
}

func unwrapDNSError(err error) (*net.DNSError, bool) {
	var dnsErr *net.DNSError
	return dnsErr, errors.As(err, &dnsErr)
}

// OwnsAccount reports whether this bot still speaks for the op's source
// account — a credential swap to another Telegram bot must not deliver the
// old account's replies.
func (b *Bot) OwnsAccount(accountKey string) bool {
	return b.bot != nil && b.bot.Me != nil && b.bot.Me.Username == accountKey
}

// SendDraftUpdate implements channel.DraftSender: one message edited in
// place while the run executes. Drafts send as plain text — mid-turn
// markdown is half-formed — and the reply op finalizes the same message.
func (b *Bot) SendDraftUpdate(_ context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.DraftUpdatePayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: bad draft payload: %v", err)
	}
	text := tailRunes(payload.Text, telegramMaxMessageLen)
	if strings.TrimSpace(text) == "" {
		text = "…"
	}
	chat := teleChatForKey(op.Address.ChatKey)
	if chat == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: empty chat key")
	}
	if op.DraftMessageID != "" {
		editable := tele.StoredMessage{MessageID: op.DraftMessageID, ChatID: mustChatID(chat)}
		_, err := b.bot.Edit(editable, text)
		if err == nil || isNotModified(err) {
			return channel.SendResult{PlatformMessageID: op.DraftMessageID}, nil
		}
		return channel.SendResult{}, classifySend(err)
	}
	opts := &tele.SendOptions{}
	if op.Address.ThreadKey != "" {
		if id, err := strconv.Atoi(op.Address.ThreadKey); err == nil {
			opts.ThreadID = id
		}
	}
	if op.Address.ReplyToKey != "" {
		if id, err := strconv.Atoi(op.Address.ReplyToKey); err == nil {
			opts.ReplyTo = &tele.Message{ID: id, Chat: &tele.Chat{ID: mustChatID(chat)}}
		}
	}
	msg, err := b.bot.Send(chat, text, opts)
	if err != nil {
		return channel.SendResult{}, classifySend(err)
	}
	return channel.SendResult{PlatformMessageID: strconv.Itoa(msg.ID)}, nil
}

// isNotModified is Telegram's no-op edit receipt: the snapshot is already on
// the message, so the attempt is confirmed, not a failure.
func isNotModified(err error) bool {
	var apiErr *tele.Error
	return errors.As(err, &apiErr) && strings.Contains(apiErr.Description, "message is not modified")
}

// tailRunes keeps the last max runes so a progress message stays inside the
// platform limit without cutting mid-token in the head.
func tailRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[len(runes)-max:])
}

// sendReplyOp delivers the reply's primary segment — payload.Text, already
// split to the platform budget at enqueue time. Overflow chunks and
// attachments are sibling ops with their own receipts, so this handler makes
// exactly one platform call: edit the live draft to its final content, or
// send the first segment as a new message.
func (b *Bot) sendReplyOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.ReplyOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: bad send_reply payload: %v", err)
	}
	chat := teleChatForKey(op.Address.ChatKey)
	if chat == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: empty chat key")
	}
	text := payload.Text
	if strings.TrimSpace(text) == "" {
		text = "(empty response)"
	}
	if op.DraftMessageID != "" {
		// The live draft becomes the reply's first segment — the message the
		// user watched during the turn is the final reply, not a sibling.
		editable := tele.StoredMessage{MessageID: op.DraftMessageID, ChatID: mustChatID(chat)}
		_, err := b.bot.Edit(editable, renderMarkdown(b.md, text), tele.ModeMarkdownV2)
		if err != nil && !isNotModified(err) && !isTelegramTransport(err) && !isTelegramFlood(err) {
			// The platform answered, so the markdown edit provably did not
			// land — fall back to a plain-text edit once. Second call in the
			// same op: a fenced-out owner must not edit.
			if gerr := op.CheckOwnership(ctx); gerr != nil {
				return channel.SendResult{}, gerr
			}
			_, err = b.bot.Edit(editable, text)
		}
		if err != nil && !isNotModified(err) {
			return channel.SendResult{}, classifySend(err)
		}
		return channel.SendResult{PlatformMessageID: op.DraftMessageID}, nil
	}
	opts := &tele.SendOptions{}
	if op.Address.ThreadKey != "" {
		if id, err := strconv.Atoi(op.Address.ThreadKey); err == nil {
			opts.ThreadID = id
		}
	}
	if op.Address.ReplyToKey != "" {
		if id, err := strconv.Atoi(op.Address.ReplyToKey); err == nil {
			opts.ReplyTo = &tele.Message{ID: id, Chat: &tele.Chat{ID: mustChatID(chat)}}
		}
	}
	msg, err := b.sendTelegramMarkdown(ctx, chat, text, opts, op.CheckOwnership)
	if err != nil {
		return channel.SendResult{}, classifySend(err)
	}
	id := ""
	if msg != nil {
		id = strconv.Itoa(msg.ID)
	}
	return channel.SendResult{PlatformMessageID: id}, nil
}

// sendAttachmentOp delivers one image or file — one durable op, one platform
// call, one receipt row.
func (b *Bot) sendAttachmentOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.AttachmentOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: bad send_attachment payload: %v", err)
	}
	chat := teleChatForKey(op.Address.ChatKey)
	if chat == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: empty chat key")
	}
	opts := &tele.SendOptions{}
	var err error
	switch payload.Kind {
	case channel.AttachmentImage:
		err = b.sendGroupImage(ctx, chat, channel.ImageEvent{Data: payload.Data, MimeType: payload.MimeType}, opts)
	case channel.AttachmentFile:
		err = b.sendGroupFile(ctx, chat, channel.FileEvent{Path: payload.Path, Name: payload.Name}, opts)
	default:
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: unknown attachment kind %q", payload.Kind)
	}
	if err != nil {
		return channel.SendResult{}, classifySend(err)
	}
	return channel.SendResult{}, nil
}

func mustChatID(chat tele.Recipient) int64 {
	if c, ok := chat.(*tele.Chat); ok {
		return c.ID
	}
	return 0
}
