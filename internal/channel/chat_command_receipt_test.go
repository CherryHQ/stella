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
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ('cmd-agent', 'cmd', '/tmp/cmd-agent')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	c := &Coordinator{db: db, queue: newSessionQueue()}
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	msg := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", MessageID: "dm-msg-7"}

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
	next := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", MessageID: "dm-msg-8"}
	if reply := c.handleNewSessionCommand(ctx, rc, next); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("second command reply = %q, want a fresh rotation", reply)
	}

	// The same message id on a different channel instance is a different
	// message: platform message ids are only unique within one instance.
	otherBot := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-b", MessageID: "dm-msg-7"}
	if reply := c.handleNewSessionCommand(ctx, rc, otherBot); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("other-instance reply = %q, want a fresh rotation", reply)
	}
}

// TestDMNewReceiptIsInertWithoutAMessageID mirrors the group rule: a delivery
// Stella cannot name runs unguarded rather than collapsing every such message
// onto one shared row.
func TestDMNewReceiptIsInertWithoutAMessageID(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ('cmd-agent', 'cmd', '/tmp/cmd-agent')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	c := &Coordinator{db: db, queue: newSessionQueue()}
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})

	for i := range 2 {
		msg := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a"}
		if reply := c.handleNewSessionCommand(ctx, rc, msg); reply != pkgchannel.NewSessionStartedMessage {
			t.Fatalf("/new #%d reply = %q, want a rotation", i, reply)
		}
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_chat_command_receipt`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if count != 0 {
		t.Fatalf("receipts written for an unidentifiable message = %d, want 0", count)
	}
}
