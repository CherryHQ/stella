package qq

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/errs"

	"github.com/CherryHQ/stella/pkg/channel"
)

// SendOperation implements channel.OperationSender: one durable op maps to one
// QQ open-api post. The outbox Scope picks C2C vs group because QQ routes the
// two through different endpoints with the same open-id shape.
func (b *Bot) SendOperation(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	if op.Kind == "send_group_reply" {
		var payload channel.GroupReplyOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "qq: bad send_group_reply payload: %v", err)
		}
		if err := b.Publish(ctx, payload.PublishRequest()); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "qq: group reply publish: %v", err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind == "notify" {
		var payload struct {
			Notification channel.Notification `json:"notification"`
		}
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "qq: bad notify payload: %v", err)
		}
		if err := b.Notify(ctx, payload.Notification); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "qq: notify send: %v", err)
		}
		return channel.SendResult{}, nil
	}
	if op.Kind != "send_text" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "qq: unsupported op kind %q", op.Kind)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "qq: bad send_text payload: %v", err)
	}
	if op.Address.ChatKey == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "qq: empty chat key")
	}
	if b.api == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "qq: api client not initialized")
	}
	msg := dto.MessageToCreate{
		Content: payload.Text,
		MsgType: dto.TextMsg,
		MsgID:   op.Address.ReplyToKey,
	}
	var sent *dto.Message
	var err error
	if op.Address.Scope == "group" {
		sent, err = b.api.PostGroupMessage(ctx, op.Address.ChatKey, msg)
	} else {
		sent, err = b.api.PostC2CMessage(ctx, op.Address.ChatKey, msg)
	}
	if err != nil {
		return channel.SendResult{}, classifyQQSend(err)
	}
	id := ""
	if sent != nil {
		id = sent.ID
	}
	return channel.SendResult{PlatformMessageID: id}, nil
}

// classifyQQSend maps a botgo failure to a ledger class. Platform error codes
// are platform-specific ints — 5xx-band codes and transport errors that may
// have landed are unknown/retryable; the rest of a real API response is
// permanent.
func classifyQQSend(err error) error {
	var sdkErr *errs.Err
	if errors.As(err, &sdkErr) {
		code := sdkErr.Code()
		switch {
		case code >= 500 && code < 600:
			return &channel.SendError{Class: channel.SendRetryable, Err: err}
		case code == 9999:
			// SDK-side wrap of a non-API failure — treat like transport.
			return &channel.SendError{Class: channel.SendUnknown, Err: err}
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
	return &channel.SendError{Class: channel.SendUnknown, Err: err}
}

// OwnsAccount checks the app id captured at receive time.
func (b *Bot) OwnsAccount(accountKey string) bool {
	return b.cfg.AppID != "" && b.cfg.AppID == accountKey
}
