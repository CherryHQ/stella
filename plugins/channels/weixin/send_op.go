package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/channel"
)

// SendOperation implements channel.OperationSender. The address carries the
// user id as ChatKey and the context_token as Token — a replica that never
// saw the inbound message can still reply. ClientID is deterministic per op
// so a replayed attempt after a lost response dedupes on the platform side.
func (b *Bot) SendOperation(ctx context.Context, op channel.OutboundOp) (channel.SendResult, error) {
	if op.Kind == "notify" {
		var payload struct {
			Notification channel.Notification `json:"notification"`
		}
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: bad notify payload: %v", err)
		}
		n := payload.Notification
		target := n.ChatID
		if target == "" {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: no target user ID for notification")
		}
		if b.client == nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendUnknown, "weixin: client not initialized")
		}
		tokenVal, ok := b.contextTokens.Load(target)
		if !ok {
			// The reply credential is repopulated when the user messages
			// again — retryable, a later sweep can deliver.
			return channel.SendResult{}, channel.SendErrorf(channel.SendRetryable, "weixin: no context_token for user %s", target)
		}
		contextToken, _ := tokenVal.(string)
		if err := op.CheckOwnership(ctx); err != nil {
			return channel.SendResult{}, err
		}
		msg := WeixinMessage{
			ToUserID:     target,
			ClientID:     deterministicClientID(op.DeliveryKey, op.OperationIndex),
			MessageType:  MessageTypeBot,
			MessageState: MessageStateFinish,
			ContextToken: contextToken,
			ItemList: []MessageItem{{
				Type:     ItemTypeText,
				TextItem: &TextItem{Text: n.Text},
			}},
		}
		if err := b.client.SendMessage(msg); err != nil {
			return channel.SendResult{}, classifyWeixinSend(err)
		}
		return channel.SendResult{PlatformMessageID: msg.ClientID}, nil
	}
	if op.Kind == "send_reply" {
		var payload channel.ReplyOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: bad send_reply payload: %v", err)
		}
		if op.Address.ChatKey == "" || b.client == nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: empty chat key or client down")
		}
		// The op delivers exactly the pre-split primary segment; overflow
		// chunks and attachments are sibling ops with their own receipts.
		text := payload.Text
		if strings.TrimSpace(text) == "" {
			text = "(empty response)"
		}
		msg := WeixinMessage{
			ToUserID:     op.Address.ChatKey,
			ClientID:     deterministicClientID(op.DeliveryKey, op.OperationIndex),
			MessageType:  MessageTypeBot,
			MessageState: MessageStateFinish,
			ContextToken: op.Address.Token,
			ItemList: []MessageItem{{
				Type:     ItemTypeText,
				TextItem: &TextItem{Text: text},
			}},
		}
		if err := b.client.SendMessage(msg); err != nil {
			return channel.SendResult{}, classifyWeixinSend(err)
		}
		return channel.SendResult{PlatformMessageID: msg.ClientID}, nil
	}
	if op.Kind == "send_attachment" {
		var payload channel.AttachmentOpPayload
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: bad send_attachment payload: %v", err)
		}
		if op.Address.ChatKey == "" || b.client == nil {
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: empty chat key or client down")
		}
		// The op address token re-seeds the reply credential on a replica that
		// never saw inbound. The deterministic client_id dedupes the platform
		// message even if the CDN upload had to repeat mid-retry.
		if op.Address.Token != "" {
			b.contextTokens.Store(op.Address.ChatKey, op.Address.Token)
		}
		clientID := deterministicClientID(op.DeliveryKey, op.OperationIndex)
		msg := WeixinMessage{FromUserID: op.Address.ChatKey}
		var err error
		switch payload.Kind {
		case channel.AttachmentImage:
			err = b.sendImage(msg, channel.ImageEvent{Data: payload.Data, MimeType: payload.MimeType}, clientID,
				func() error { return op.CheckOwnership(ctx) })
		case channel.AttachmentFile:
			data, rerr := os.ReadFile(payload.Path)
			if rerr != nil {
				return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: read attachment %s: %v", payload.Path, rerr)
			}
			err = b.sendFile(msg, payload.Name, data, clientID,
				func() error { return op.CheckOwnership(ctx) })
		default:
			return channel.SendResult{}, channel.SendErrorf(channel.SendPermanent, "weixin: unknown attachment kind %q", payload.Kind)
		}
		if err != nil {
			return channel.SendResult{}, classifyWeixinSend(err)
		}
		return channel.SendResult{PlatformMessageID: clientID}, nil
	}
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
	var se *channel.SendError
	if errors.As(err, &se) {
		return se
	}
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
