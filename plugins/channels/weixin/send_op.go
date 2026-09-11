package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/channel"
)

// SendOperation implements channel.OperationSender. The address carries the
// user id as ChatKey and the context_token as Token — a replica that never
// saw the inbound message can still reply. ClientID is deterministic per op
// so a replayed attempt after a lost response dedupes on the platform side.
func (b *Bot) SendOperation(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	if op.Kind != "send_text" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: unsupported op kind %q", op.Kind)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: bad send_text payload: %v", err)
	}
	if op.Address.ChatKey == "" {
		return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: empty chat key")
	}
	if b.client == nil {
		return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "weixin: client not initialized")
	}
	msg := WeixinMessage{
		ToUserID:     op.Address.ChatKey,
		ClientID:     deterministicClientID(op.DeliveryKey, op.OperationIndex),
		MessageType:  MessageTypeBot,
		MessageState: MessageStateFinish,
		ContextToken: op.Address.Token,
		ItemList: []MessageItem{{
			Type:     ItemTypeText,
			TextItem: &TextItem{Text: payload.Text},
		}},
	}
	if err := b.client.SendMessage(msg); err != nil {
		return channel.SendResult{}, classifyWeixinSend(err)
	}
	// The iLink API does not return a message id; the deterministic client_id
	// is the delivery identity we can report back.
	return channel.SendResult{PlatformMessageID: msg.ClientID}, nil
}

func deterministicClientID(deliveryKey string, index int) string {
	return "obx-" + uuid.NewSHA1(uuid.NameSpaceURL, []byte(deliveryKey+":"+strconv.Itoa(index))).String()[:16]
}

// classifyWeixinSend maps iLink failures: session expiry is permanent until
// re-auth; transport failures are unknown.
func classifyWeixinSend(err error) error {
	if errors.Is(err, ErrSessionExpired) {
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
	return &channel.SendError{Class: channel.SendUnknown, Err: err}
}

// OwnsAccount checks the ilink bot id captured at receive time.
func (b *Bot) OwnsAccount(accountKey string) bool {
	return b.cfg.BotID != "" && b.cfg.BotID == accountKey
}
