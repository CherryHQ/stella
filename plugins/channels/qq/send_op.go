package qq

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"

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
	if op.Kind == "send_reply" {
		return b.sendReplyOp(ctx, op)
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

// A QQ draft's durable receipt encodes the stream id and its last posted
// chunk index — "streamID:index" — so a successor op (or the terminal
// finalize) resumes the same stream on whichever replica owns the lease.
func encodeStreamCursor(streamID string, index uint32) string {
	return streamID + ":" + strconv.FormatUint(uint64(index), 10)
}

func decodeStreamCursor(s string) (streamID string, index uint32, ok bool) {
	id, raw, found := strings.Cut(s, ":")
	if !found || id == "" {
		return "", 0, false
	}
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return "", 0, false
	}
	return id, uint32(n), true
}

// SendDraftUpdate implements channel.DraftSender: each draft op is one QQ
// stream chunk — first call creates the stream, later calls continue it.
func (b *Bot) SendDraftUpdate(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.DraftUpdatePayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "qq: bad draft payload: %v", err)
	}
	targetID := op.Address.ChatKey
	if targetID == "" || b.api == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "qq: empty chat key or api down")
	}
	scope := scopeC2C
	if op.Address.Scope == "group" {
		scope = scopeGroup
	}
	streamID, index, _ := decodeStreamCursor(op.DraftMessageID)
	newMsgID, err := b.sendStreamChunk(ctx, targetID, op.Address.ReplyToKey, buildStreamDisplay(payload.Text, ""), streamID, index+1, false, scope)
	if err != nil {
		return channel.SendResult{}, classifyQQSend(err)
	}
	if streamID == "" {
		streamID = newMsgID
	}
	if streamID == "" {
		// The platform gave no stream id back — the stream cannot continue,
		// so this attempt must not report a cursor that would chain nothing.
		return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "qq: stream chunk returned no message id")
	}
	return channel.SendResult{PlatformMessageID: encodeStreamCursor(streamID, index+1)}, nil
}

// sendReplyOp posts the terminal stream chunk — State=10 ends the "generating"
// state on the same stream the draft ops opened. The full reply text rides
// the sibling send_text chain, so this op is exactly one platform call. A run
// that never produced a draft has no stream to close; the op is a no-op.
func (b *Bot) sendReplyOp(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	var payload channel.ReplyOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "qq: bad send_reply payload: %v", err)
	}
	streamID, index, ok := decodeStreamCursor(op.DraftMessageID)
	if !ok {
		return channel.SendResult{PlatformMessageID: op.DraftMessageID}, nil
	}
	targetID := op.Address.ChatKey
	if targetID == "" || b.api == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "qq: empty chat key or api down")
	}
	scope := scopeC2C
	if op.Address.Scope == "group" {
		scope = scopeGroup
	}
	response, _, _ := channel.CollectReplyEvents(payload.Events)
	if strings.TrimSpace(response) == "" {
		response = "(empty response)"
	}
	if err := op.CheckOwnership(ctx); err != nil {
		return channel.SendResult{}, err
	}
	if _, err := b.sendStreamChunk(ctx, targetID, op.Address.ReplyToKey, buildStreamDisplay(response, ""), streamID, index+1, true, scope); err != nil {
		return channel.SendResult{}, classifyQQSend(err)
	}
	return channel.SendResult{PlatformMessageID: op.DraftMessageID}, nil
}
