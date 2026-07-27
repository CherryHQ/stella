package channel

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// TestDMNewRedeliveryRotatesOnce is the DM counterpart of
// TestGroupNewRedeliveryRotatesOnce. DMs have no group event log, so the
// receipt is the only thing standing between a platform redelivery of `/new`
// and a second rotation that would silently archive everything said since.
func TestDMNewRedeliveryRotatesOnce(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	c := &Coordinator{db: db, queue: newSessionQueue()}
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	msg := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", SenderID: "tg-acct-1", MessageID: "dm-msg-7"}

	if reply := c.handleNewSessionCommand(ctx, rc, msg); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("first /new reply = %q, want %q", reply, pkgchannel.NewSessionStartedMessage)
	}
	rotated, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}

	// The redelivery arrives after the chat has kept talking, so the stale-CAS
	// guard would not catch it — only the receipt does.
	if reply := c.handleNewSessionCommand(ctx, rc, msg); reply != pkgchannel.SessionAlreadyResetMessage {
		t.Fatalf("redelivered /new reply = %q, want %q", reply, pkgchannel.SessionAlreadyResetMessage)
	}
	after, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession after redelivery: %v", err)
	}
	if after.ID != rotated.ID {
		t.Fatalf("a redelivered DM /new rotated again: %q -> %q", rotated.ID, after.ID)
	}

	// A genuinely new message still rotates.
	next := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", SenderID: "tg-acct-1", MessageID: "dm-msg-8"}
	if reply := c.handleNewSessionCommand(ctx, rc, next); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("second command reply = %q, want a fresh rotation", reply)
	}

	// The same message id on a different channel instance is a different
	// message: platform message ids are only unique within one instance.
	otherBot := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-b", SenderID: "tg-acct-1", MessageID: "dm-msg-7"}
	if reply := c.handleNewSessionCommand(ctx, rc, otherBot); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("other-instance reply = %q, want a fresh rotation", reply)
	}
}

// TestDMNewReceiptSurvivesAgentSwitch pins the receipt to the message's
// physical identity, not its routing: the same platform message redelivered
// after the user switches agents computes a different chat binding, and a
// binding-keyed claim would happily rotate the new agent's session — a reset
// the user never asked for.
func TestDMNewReceiptSurvivesAgentSwitch(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	c := &Coordinator{db: db, queue: newSessionQueue()}
	msg := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", SenderID: "tg-acct-1", MessageID: "dm-msg-7"}

	rcA := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	if reply := c.handleNewSessionCommand(ctx, rcA, msg); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("first /new reply = %q, want %q", reply, pkgchannel.NewSessionStartedMessage)
	}

	// Same physical message, now routed to a different agent.
	rcB := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	rcB.AgentID = "cmd-agent-b"
	rcB.SessionKey = "cmd-agent-b:user:user-1:private"
	rcB.Channel = "cmd-agent-b:user:user-1:private"
	if rcB.queueKey() == rcA.queueKey() {
		t.Fatal("test needs the routing to differ between deliveries")
	}
	beforeB, err := rcB.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession for agent B: %v", err)
	}
	if reply := c.handleNewSessionCommand(ctx, rcB, msg); reply != pkgchannel.SessionAlreadyResetMessage {
		t.Fatalf("redelivery after agent switch reply = %q, want %q", reply, pkgchannel.SessionAlreadyResetMessage)
	}
	afterB, err := rcB.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession after redelivery: %v", err)
	}
	if afterB.ID != beforeB.ID {
		t.Fatal("a redelivered /new rotated the newly-routed agent's session")
	}
}

// TestDMNewDistinctAccountsAreDistinctMessages covers the other half of the
// physical identity: one linked Stella user can own several platform accounts
// on one bot, and platform message ids are only unique per chat — so the same
// id from a different account is a different message whose `/new` must run.
func TestDMNewDistinctAccountsAreDistinctMessages(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	c := &Coordinator{db: db, queue: newSessionQueue()}
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})

	first := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", SenderID: "tg-acct-1", MessageID: "dm-msg-7"}
	if reply := c.handleNewSessionCommand(ctx, rc, first); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("account 1 reply = %q, want %q", reply, pkgchannel.NewSessionStartedMessage)
	}
	second := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", SenderID: "tg-acct-2", MessageID: "dm-msg-7"}
	if reply := c.handleNewSessionCommand(ctx, rc, second); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("account 2 reply = %q, want a fresh rotation (message ids are per-chat)", reply)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_chat_command_receipt`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 2 {
		t.Fatalf("receipts = %d, want 2 distinct claims", count)
	}
}

// TestDMNewFailsClosedWithoutAMessageID mirrors the group rule: a delivery
// Stella cannot name could be a redelivery of a `/new` that already ran, and
// the reset is destructive, so it refuses to run instead of running unguarded.
func TestDMNewFailsClosedWithoutAMessageID(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	c := &Coordinator{db: db, queue: newSessionQueue()}
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})

	before, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	msg := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", SenderID: "tg-acct-1"}
	if reply := c.handleNewSessionCommand(ctx, rc, msg); reply != pkgchannel.NewSessionUnverifiableMessage {
		t.Fatalf("/new without a message id reply = %q, want %q", reply, pkgchannel.NewSessionUnverifiableMessage)
	}
	after, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession after refusal: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("an unidentifiable /new rotated the session: %q -> %q", before.ID, after.ID)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_chat_command_receipt`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 0 {
		t.Fatalf("receipts written for an unidentifiable message = %d, want 0", count)
	}
}
