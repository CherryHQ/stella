package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"

	"github.com/CherryHQ/stella/pkg/channel"
)

// SendOperation implements channel.OperationSender. DingTalk's only outbound
// credential is the per-conversation session webhook, carried on the op
// address token. A missing webhook is permanent — no retry can invent one.
func (b *Bot) SendOperation(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	if op.Kind == "notify" {
		var payload struct {
			Notification channel.Notification `json:"notification"`
		}
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "dingtalk: bad notify payload: %v", err)
		}
		if err := b.Notify(ctx, payload.Notification); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "dingtalk: notify send: %v", err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind != "send_text" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "dingtalk: unsupported op kind %q", op.Kind)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "dingtalk: bad send_text payload: %v", err)
	}
	if op.Address.Token == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "dingtalk: no session webhook for op")
	}
	if err := sendWebhookText(ctx, op.Address.Token, payload.Text); err != nil {
		return channel.SendResult{}, classifyDingTalkSend(err)
	}
	// Session webhooks return no message id.
	return channel.SendResult{}, nil
}

// classifyDingTalkSend maps webhook outcomes: HTTP errors and errcode
// rejections are real platform answers; transport failures are unknown.
func classifyDingTalkSend(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "webhook returned") || strings.Contains(msg, "webhook rejected") {
		// A real webhook response: 5xx may retry, the rest is permanent —
		// an expired session webhook never recovers.
		if strings.Contains(msg, " 5") && !strings.Contains(msg, "errcode") {
			return &channel.SendError{Class: channel.SendRetryable, Err: err}
		}
		return &channel.SendError{Class: channel.SendPermanent, Err: err}
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

// OwnsAccount checks the callback's chatbot user id against the bots this
// instance has registered (DingTalk may expose several).
func (b *Bot) OwnsAccount(accountKey string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.registeredBots[accountKey]
	return ok
}
