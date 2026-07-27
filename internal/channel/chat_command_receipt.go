package channel

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// chatCommandReceipt is the DM counterpart of commandReceipt: the durable
// "this inbound message's command already ran" marker for one private-chat
// command. DMs have no group event log, so without it a platform redelivery of
// `/new` would re-resolve the successor session and rotate it again.
//
// Inert on missing pieces for the same reason as the group receipt: a delivery
// Stella cannot name runs unguarded rather than collapsing onto a shared row.
type chatCommandReceipt struct {
	q         *sqlc.Queries
	agentID   string
	binding   string
	channelID string
	messageID string
	command   string
}

// newChatCommandReceipt scopes a claim to the chat's durable queue/binding key
// and the channel instance the message arrived on. The instance — not the
// platform — is the message id's namespace: two bots on the same platform can
// both count message ids from 1.
func newChatCommandReceipt(q *sqlc.Queries, rc *ResolvedChat, channelID, messageID, command string) chatCommandReceipt {
	return chatCommandReceipt{
		q:         q,
		agentID:   rc.AgentID,
		binding:   rc.queueKey(),
		channelID: channelID,
		messageID: messageID,
		command:   command,
	}
}

func (r chatCommandReceipt) inert() bool {
	return r.q == nil || r.binding == "" || r.channelID == "" || r.messageID == ""
}

// claim reserves the right to run the command once; false means a redelivery
// of a message whose command has already run. Same contract as the group
// receipt: claimed before the command runs, consumed claims are permanent.
func (r chatCommandReceipt) claim(ctx context.Context) (bool, error) {
	if r.inert() {
		return true, nil
	}
	rows, err := r.q.CreateChatCommandReceipt(ctx, sqlc.CreateChatCommandReceiptParams{
		AgentID:   r.agentID,
		Binding:   r.binding,
		ChannelID: r.channelID,
		MessageID: r.messageID,
		Command:   r.command,
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
		Binding:   r.binding,
		ChannelID: r.channelID,
		MessageID: r.messageID,
	}); err != nil {
		slog.WarnContext(ctx, "failed to release chat command receipt", "error", err,
			"binding", r.binding, "channel_id", r.channelID)
	}
}
