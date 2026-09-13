package channel

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// Durable-route port of the legacy /new receipt tests: the inbox's physical
// dedup key is now the first line against redelivery, and the command receipt
// still fences a rotation across channel-instance boundaries.

// routeReply sends one command event through the durable path and returns the
// reply op's text (empty when nothing was replied).
func routeReply(t *testing.T, c *Coordinator, channelID string, msg pkgchannel.IncomingMessage, command string) string {
	t.Helper()
	ctx := context.Background()
	if _, _, _, err := c.HandleIncoming(ctx, msg, command, ""); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if _, err := c.RoutePending(ctx, channelID); err != nil {
		t.Fatalf("route: %v", err)
	}
	var text string
	err := c.db.QueryRow(ctx,
		`SELECT payload->>'text' FROM channel_outbox ORDER BY created_at DESC LIMIT 1`).Scan(&text)
	if err != nil {
		t.Fatalf("read reply op: %v", err)
	}
	return text
}

func TestDMNewRedeliveryRotatesOnce(t *testing.T) {
	c, ts := setupRouter(t)
	ctx := context.Background()
	linkTelegramUser(t, ts, "new@example.com", "tg-acct-1")
	if err := ts.store.CreateChannel(ctx, config.Channel{ID: "tg-bot-a", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatal(err)
	}
	msg := pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", SenderID: "tg-acct-1", MessageID: "dm-msg-7"}

	if got := routeReply(t, c, "tg-bot-a", msg, "/new"); got != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("first /new reply = %q", got)
	}
	var firstSession string
	if err := c.db.QueryRow(ctx, `SELECT id FROM ctx_conversation ORDER BY created_at DESC LIMIT 1`).Scan(&firstSession); err != nil {
		t.Fatalf("read session: %v", err)
	}

	// Platform redelivery: same event key dedups at the inbox — no second
	// rotation, no second reply op.
	outboxBefore := countOutbox(t, c)
	if _, _, _, err := c.HandleIncoming(ctx, msg, "/new", ""); err != nil {
		t.Fatal(err)
	}
	if n, err := c.RoutePending(ctx, "tg-bot-a"); err != nil || n != 0 {
		t.Fatalf("redelivery routed %d events, want 0", n)
	}
	if countOutbox(t, c) != outboxBefore {
		t.Fatal("redelivered /new produced a second reply")
	}
	var secondSession string
	if err := c.db.QueryRow(ctx, `SELECT id FROM ctx_conversation ORDER BY created_at DESC LIMIT 1`).Scan(&secondSession); err != nil {
		t.Fatal(err)
	}
	if secondSession != firstSession {
		t.Fatalf("redelivered /new rotated: %q -> %q", firstSession, secondSession)
	}

	// A genuinely new message still rotates.
	if got := routeReply(t, c, "tg-bot-a", pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", SenderID: "tg-acct-1", MessageID: "dm-msg-8"}, "/new"); got != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("fresh /new reply = %q", got)
	}
}

// The same platform message id on a different channel instance is a different
// message — each gets its own rotation.
func TestDMNewDistinctChannelsAreDistinctMessages(t *testing.T) {
	c, ts := setupRouter(t)
	ctx := context.Background()
	linkTelegramUser(t, ts, "new2@example.com", "tg-acct-1")
	for _, id := range []string{"tg-bot-a", "tg-bot-b"} {
		if err := ts.store.CreateChannel(ctx, config.Channel{ID: id, Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
			t.Fatal(err)
		}
	}
	msg := func(ch string) pkgchannel.IncomingMessage {
		return pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: ch, SenderID: "tg-acct-1", MessageID: "dm-msg-7"}
	}
	if got := routeReply(t, c, "tg-bot-a", msg("tg-bot-a"), "/new"); got != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("bot-a reply = %q", got)
	}
	if got := routeReply(t, c, "tg-bot-b", msg("tg-bot-b"), "/new"); got != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("bot-b reply = %q", got)
	}
}

// A /new that cannot name its message could be a redelivery of one that
// already ran — the reset is destructive, so it refuses closed.
func TestDMNewFailsClosedWithoutAMessageID(t *testing.T) {
	c, ts := setupRouter(t)
	ctx := context.Background()
	linkTelegramUser(t, ts, "new3@example.com", "tg-acct-1")
	if err := ts.store.CreateChannel(ctx, config.Channel{ID: "tg-bot-a", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatal(err)
	}
	got := routeReply(t, c, "tg-bot-a", pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "tg-bot-a", SenderID: "tg-acct-1"}, "/new")
	if got != pkgchannel.NewSessionUnverifiableMessage {
		t.Fatalf("/new without message id reply = %q, want %q", got, pkgchannel.NewSessionUnverifiableMessage)
	}
	var n int
	if err := c.db.QueryRow(ctx, `SELECT count(*) FROM agent_run`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("agent runs = %d, want 0 (rotation must not execute)", n)
	}
}
