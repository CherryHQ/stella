package channel

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// InboundMessageDeduper is the durable claim boundary that a channel uses
// before handing an ordinary platform event to the coordinator. Commands keep
// their permanent receipts; ordinary messages expire after the platform retry
// window so the table cannot become chat history.
type InboundMessageDeduper interface {
	ClaimInboundMessage(context.Context, pkgchannel.IncomingMessage) (bool, error)
	ReleaseInboundMessage(context.Context, pkgchannel.IncomingMessage)
}

type inboundMessageReceipt struct {
	q         *sqlc.Queries
	channelID string
	chatKey   string
	messageID string
}

func inboundReceiptForMessage(q *sqlc.Queries, msg pkgchannel.IncomingMessage) inboundMessageReceipt {
	channelID, chatKey := messageDeliveryCoordinates(msg)
	return inboundMessageReceipt{q: q, channelID: channelID, chatKey: chatKey, messageID: msg.MessageID}
}

func (r inboundMessageReceipt) inert() bool {
	return r.q == nil || r.channelID == "" || r.chatKey == "" || r.messageID == ""
}

func (r inboundMessageReceipt) claim(ctx context.Context) (bool, error) {
	if r.inert() {
		return true, nil
	}
	if _, err := r.q.DeleteExpiredInboundMessageReceipt(ctx); err != nil {
		return false, fmt.Errorf("delete expired inbound message receipts: %w", err)
	}
	rows, err := r.q.CreateInboundMessageReceipt(ctx, sqlc.CreateInboundMessageReceiptParams{
		ChannelID: r.channelID,
		ChatKey:   r.chatKey,
		MessageID: r.messageID,
	})
	if err != nil {
		return false, fmt.Errorf("claim inbound message receipt: %w", err)
	}
	return rows > 0, nil
}

func (r inboundMessageReceipt) release(ctx context.Context) {
	if r.inert() {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.q.DeleteInboundMessageReceipt(ctx, sqlc.DeleteInboundMessageReceiptParams{
		ChannelID: r.channelID,
		ChatKey:   r.chatKey,
		MessageID: r.messageID,
	}); err != nil {
		slog.WarnContext(ctx, "failed to release inbound message receipt", "error", err,
			"channel_id", r.channelID, "chat_key", r.chatKey)
	}
}

// ClaimInboundMessage implements InboundMessageDeduper for channel plugins.
func (c *Coordinator) ClaimInboundMessage(ctx context.Context, msg pkgchannel.IncomingMessage) (bool, error) {
	return inboundReceiptForMessage(c.receiptQueries(), msg).claim(ctx)
}

// ReleaseInboundMessage implements InboundMessageDeduper for channel plugins.
func (c *Coordinator) ReleaseInboundMessage(ctx context.Context, msg pkgchannel.IncomingMessage) {
	inboundReceiptForMessage(c.receiptQueries(), msg).release(ctx)
}
