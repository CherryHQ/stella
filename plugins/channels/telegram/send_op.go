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
		var payload channel.GroupReplyOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: bad send_group_reply payload: %v", err)
		}
		if err := b.Publish(ctx, payload.PublishRequest()); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "telegram: group reply publish: %v", err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind == "notify" {
		var payload struct {
			Notification channel.Notification `json:"notification"`
		}
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: bad notify payload: %v", err)
		}
		if err := b.Notify(ctx, payload.Notification); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "telegram: notify send: %v", err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind == "send_reply" {
		return b.sendReplyOp(ctx, op)
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

	rendered := renderMarkdown(b.md, payload.Text)
	msg, err := b.bot.Send(chat, rendered, opts)
	if err != nil {
		// Mirrors the live path: a markdown send rejection falls back to plain
		// text once. A classified platform error (4xx) is also tried as plain
		// text because the rejection may be markdown-specific; a plain-text
		// 4xx then classifies permanently on its own merits.
		var apiErr *tele.Error
		if errors.As(err, &apiErr) && apiErr.Code >= 500 {
			return channel.SendResult{}, classifySend(err)
		}
		if errors.As(err, &apiErr) {
			msg, err = b.bot.Send(chat, payload.Text, &tele.SendOptions{ThreadID: opts.ThreadID, ReplyTo: opts.ReplyTo})
		} else {
			return channel.SendResult{}, classifySend(err)
		}
	}
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
func classifySend(err error) error {
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

// sendReplyOp delivers a completed turn's recorded events: flattened text in
// message-sized chunks, then attachments — the same contract group publish
// uses. Telegram's live DM draft surface needs the inbound tele.Context; a
// cross-replica replay has none, so the op sends the terminal content only.
func (b *Bot) sendReplyOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.ReplyOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: bad send_reply payload: %v", err)
	}
	chat := teleChatForKey(op.Address.ChatKey)
	if chat == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "telegram: empty chat key")
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

	text, images, files := channel.CollectReplyEvents(payload.Events)
	if strings.TrimSpace(text) == "" {
		text = "(empty response)"
	}
	var firstID string
	chunks := channel.SplitMessage(text, telegramMaxMessageLen)
	if op.DraftMessageID != "" && len(chunks) > 0 {
		// The live draft becomes the reply's first chunk — the message the
		// user watched during the turn is the final reply, not a sibling.
		editable := tele.StoredMessage{MessageID: op.DraftMessageID, ChatID: mustChatID(chat)}
		_, err := b.bot.Edit(editable, renderMarkdown(b.md, chunks[0]), tele.ModeMarkdownV2)
		var apiErr *tele.Error
		if errors.As(err, &apiErr) && apiErr.Code >= 400 && apiErr.Code < 500 {
			_, err = b.bot.Edit(editable, chunks[0])
		}
		if err != nil && !isNotModified(err) {
			return channel.SendResult{}, classifySend(err)
		}
		firstID = op.DraftMessageID
		chunks = chunks[1:]
		opts = &tele.SendOptions{}
	}
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return channel.SendResult{}, classifySend(err)
		}
		msg, err := b.sendTelegramMarkdown(ctx, chat, chunk, opts)
		if err != nil {
			return channel.SendResult{}, classifySend(err)
		}
		if i == 0 && firstID == "" && msg != nil {
			firstID = strconv.Itoa(msg.ID)
		}
		opts = &tele.SendOptions{} // only the first chunk replies to the prompt
	}
	for _, img := range images {
		if err := b.sendGroupImage(ctx, chat, img, opts); err != nil {
			return channel.SendResult{}, classifySend(err)
		}
	}
	for _, file := range files {
		if err := b.sendGroupFile(ctx, chat, file, opts); err != nil {
			return channel.SendResult{}, classifySend(err)
		}
	}
	return channel.SendResult{PlatformMessageID: firstID}, nil
}

func mustChatID(chat tele.Recipient) int64 {
	if c, ok := chat.(*tele.Chat); ok {
		return c.ID
	}
	return 0
}
