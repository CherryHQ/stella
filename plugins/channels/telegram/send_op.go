package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/channel"
)

// SendOperation implements channel.OperationSender: one durable outbox
// operation maps to one Telegram call, with the result classified for the
// ledger — sent only on a real platform receipt, retryable when Telegram
// provably rejected it, unknown when the response never arrived.
func (b *Bot) SendOperation(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
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
