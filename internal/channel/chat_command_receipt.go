package channel

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// chatCommandReceipt is the durable "this inbound message's command already
// ran" marker for one private-chat command. A DM has no event-log append to
// carry dedup for it, so without this a platform redelivery of `/new` would
// re-resolve the successor session and rotate it again.
//
// Its identity is the message's physical delivery coordinates — channel
// instance, physical chat, message id — never routing state. Which agent the
// command executed against can change between delivery and redelivery
// (link changes and channel reconfiguration), and the same physical message must stay the same
// message across that: a routing-derived key would let a redelivery execute a
// second time against the new target.
//
// Missing pieces: no query set (a coordinator built without a database) runs
// unguarded — there is no store to guard with — but a delivery Stella cannot
// name fails the claim closed, because `/new` is destructive and without an
// identity a redelivery cannot be told apart from a new command.
type chatCommandReceipt struct {
	q         *sqlc.Queries
	channelID string
	chatKey   string
	messageID string
	command   string
	binding   string // audit only, never part of the claim's identity
}

// chatReceiptForMessage derives the receipt's physical coordinates from the
// inbound message (messageDeliveryCoordinates); one linked Stella user can own
// several platform accounts whose message ids collide, which is why the chat
// key is part of the identity.
func chatReceiptForMessage(q *sqlc.Queries, rc *ResolvedChat, msg pkgchannel.IncomingMessage, command string) chatCommandReceipt {
	channelID, chatKey := messageDeliveryCoordinates(msg)
	return chatCommandReceipt{
		q:         q,
		channelID: channelID,
		chatKey:   chatKey,
		messageID: msg.MessageID,
		command:   command,
		binding:   rc.queueKey(),
	}
}

// messageDeliveryCoordinates names the physical chat a message arrived in: the
// configured channel instance (falling back to the platform name) and the
// platform chat id (falling back to the sender's platform id — DMs on most
// platforms leave ChatID empty). The command receipt's identity derives from
// these, so a redelivered message keeps the same coordinates.
func messageDeliveryCoordinates(msg pkgchannel.IncomingMessage) (channelID, chatKey string) {
	channelID = msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	chatKey = msg.ChatID
	if chatKey == "" {
		chatKey = msg.SenderID
	}
	return channelID, chatKey
}

func (r chatCommandReceipt) inert() bool {
	return r.q == nil || r.channelID == "" || r.chatKey == "" || r.messageID == ""
}

// claim reserves the right to run the command once; false means a redelivery
// of a message whose command has already run. Claimed before the command runs,
// and consumed claims are never expired: re-running a destructive reset would
// silently discard whatever was said in between.
func (r chatCommandReceipt) claim(ctx context.Context) (bool, error) {
	if r.q == nil {
		return true, nil
	}
	if r.channelID == "" || r.chatKey == "" || r.messageID == "" {
		return false, errUnidentifiedCommand
	}
	rows, err := r.q.CreateChatCommandReceipt(ctx, sqlc.CreateChatCommandReceiptParams{
		ChannelID: r.channelID,
		ChatKey:   r.chatKey,
		MessageID: r.messageID,
		Command:   r.command,
		Binding:   r.binding,
	})
	if err != nil {
		return false, fmt.Errorf("claim chat command receipt: %w", err)
	}
	return rows > 0, nil
}

// release drops a claim whose command never ran. It is detached from the
// request context (the command often failed BECAUSE that context died) and
// best-effort: a stuck claim costs one retry of a command the user can repeat.
func (r chatCommandReceipt) release(ctx context.Context) {
	if r.inert() {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.q.DeleteChatCommandReceipt(ctx, sqlc.DeleteChatCommandReceiptParams{
		ChannelID: r.channelID,
		ChatKey:   r.chatKey,
		MessageID: r.messageID,
	}); err != nil {
		slog.WarnContext(ctx, "failed to release chat command receipt", "error", err,
			"channel_id", r.channelID, "chat_key", r.chatKey)
	}
}
