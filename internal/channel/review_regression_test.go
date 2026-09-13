package channel

import (
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
)

func TestDurableOrdinaryTextRemainsMessage(t *testing.T) {
	c, ts := setupRouter(t)
	linkTelegramUser(t, ts, "review@example.com", "tg-review")
	if err := ts.store.CreateChannel(t.Context(), config.Channel{ID: "tg-review", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatal(err)
	}
	msg := textMsg("telegram", "tg-review", "tg-review", "1", "hello world")
	// handleText passes the first word as command even without a slash; the
	// legacy coordinator uses an unrecognized command as ordinary chat input.
	if _, _, _, err := c.HandleIncoming(t.Context(), msg, "hello", "world"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RoutePending(t.Context(), "tg-review"); err != nil {
		t.Fatal(err)
	}
	var kind, state, code string
	if err := c.db.QueryRow(t.Context(), "SELECT event_kind,state,COALESCE(error_code,'') FROM channel_inbox").Scan(&kind, &state, &code); err != nil {
		t.Fatal(err)
	}
	n := countRuns(t, c)
	t.Logf("inbox_kind=%s inbox_state=%s error_code=%s run_count=%d", kind, state, code, n)
	if n != 1 {
		t.Fatal("ordinary Telegram text was routed as an unsupported command")
	}
}

func TestDurableMessageIdentityIncludesChat(t *testing.T) {
	c, ts := setupRouter(t)
	if err := ts.store.CreateChannel(t.Context(), config.Channel{ID: "tg-review", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatal(err)
	}
	for _, chat := range []string{"1001", "1002"} {
		msg := textMsg("telegram", "tg-review", chat, "1", "hello")
		msg.ChatID = chat
		if _, _, _, err := c.HandleIncoming(t.Context(), msg, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := c.db.QueryRow(t.Context(), "SELECT count(*) FROM channel_inbox").Scan(&n); err != nil {
		t.Fatal(err)
	}
	t.Logf("distinct_chats=2 same_chat_scoped_message_id=1 inbox_count=%d", n)
	if n != 2 {
		t.Fatal("messages from different Telegram chats collided in inbox dedup")
	}
}
