package channel

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// chatCommandReceipt is the DM counterpart of commandReceipt: the durable
// "this inbound message's command already ran" marker for one private-chat
// command. DMs have no group event log, so without it a platform redelivery of
// `/new` would re-resolve the successor session and rotate it again.
//
// Its identity is the message's physical delivery coordinates — channel
// instance, physical chat, message id — never routing state. Which agent the
// command executed against can change between delivery and redelivery
// (`/agent`, link changes), and the same physical message must stay the same
// message across that: a routing-derived key would let a redelivery execute a
// second time against the new target.
//
// Inert on missing pieces for the same reason as the group receipt: a delivery
// Stella cannot name runs unguarded rather than collapsing onto a shared row.
type chatCommandReceipt struct {
	q         *sqlc.Queries
	channelID string
	chatKey   string
	messageID string
	command   string
	binding   string // audit only, never part of the claim's identity
}

// chatReceiptForMessage derives the receipt's physical coordinates from the
// inbound message: the configured channel instance (falling back to the
// platform name), and the platform chat id (falling back to the sender's
// platform id — DMs on most platforms leave ChatID empty, and one linked
// Stella user can own several platform accounts whose message ids collide).
func chatReceiptForMessage(q *sqlc.Queries, rc *ResolvedChat, msg pkgchannel.IncomingMessage, command string) chatCommandReceipt {
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = msg.Platform
	}
	chatKey := msg.ChatID
	if chatKey == "" {
		chatKey = msg.SenderID
	}
	return chatCommandReceipt{
		q:         q,
		channelID: channelID,
		chatKey:   chatKey,
		messageID: msg.MessageID,
		command:   command,
		binding:   rc.queueKey(),
	}
}

func (r chatCommandReceipt) inert() bool {
	return r.q == nil || r.channelID == "" || r.chatKey == "" || r.messageID == ""
}

// claim reserves the right to run the command once; false means a redelivery
// of a message whose command has already run. Same contract as the group
// receipt: claimed before the command runs, consumed claims are permanent.
func (r chatCommandReceipt) claim(ctx context.Context) (bool, error) {
	if r.inert() {
		return true, nil
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

// release drops a claim whose command never ran. Detached from the request
// context and best-effort, exactly like the group receipt's release: a stuck
// claim costs one retry of a command the user can simply repeat.
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
