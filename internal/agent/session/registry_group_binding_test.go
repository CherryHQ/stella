package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory/lcm"
)

// TestMain hosts the embedded-Postgres lifecycle for this package. It is lazy:
// the server only starts when a test actually calls dbtest.New, so the unit
// tests in this package pay nothing for it.
func TestMain(m *testing.M) { dbtest.Main(m) }

// TestGroupBindingKeyIgnoresChannel is the F4 invariant stated directly. Group
// resolution deliberately does not filter on channel — a group's reply channel
// varies with the platform a message arrives through — so a lock key that
// includes the channel hands two concurrent turns on the SAME binding two
// different locks, and the serialization the binding depends on evaporates.
func TestGroupBindingKeyIgnoresChannel(t *testing.T) {
	a := ChannelRequest{UserID: testGroupID, AgentID: "agent1", GroupID: testGroupID, Channel: Channel("group:telegram-9")}
	b := a
	b.Channel = Channel("group:feishu-3")
	if a.bindingKey() != b.bindingKey() {
		t.Fatalf("group binding keys differ by channel:\n %q\n %q", a.bindingKey(), b.bindingKey())
	}

	// A private chat still binds on its channel, so its key must keep it: two
	// platforms are two distinct chats for the same user.
	p := ChannelRequest{UserID: "u1", AgentID: "agent1", Channel: Channel("agent1:tg:ext-1:private")}
	q := p
	q.Channel = Channel("agent1:feishu:ext-1:private")
	if p.bindingKey() == q.bindingKey() {
		t.Fatalf("two private chats share the binding key %q", p.bindingKey())
	}
}

// newSQLBackedRegistry returns a registry over the real store plus the group id
// its sessions belong to. ctx_conversation.group_id is a foreign key, so the
// group has to exist before any session can name it.
func newSQLBackedRegistry(t *testing.T, platformGroupID string) (*Registry, *pgxpool.Pool, string) {
	t.Helper()
	db := dbtest.New(t)
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("lcm.New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	reg, err := NewRegistry(p, "agent1")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	var groupID string
	if err := db.QueryRow(context.Background(),
		`INSERT INTO ctx_group_state (platform, platform_group_id) VALUES ('telegram', $1) RETURNING id::text`,
		platformGroupID).Scan(&groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	return reg, db, groupID
}

func activeGroupChatRows(t *testing.T, db *pgxpool.Pool, groupID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM ctx_conversation
		 WHERE kind = 'chat' AND archived = false AND group_id IS NOT NULL AND user_id = $1`, groupID).Scan(&n); err != nil {
		t.Fatalf("count group chat rows: %v", err)
	}
	return n
}

// TestResolveGroupChannelConcurrentlyBindsOneSession is F4 against the real
// store. Two platform turns for the same group arrive through different reply
// channels; they are the same binding, so they must produce one session. This
// fails on a channel-keyed lock, and it fails loudly rather than subtly: the
// second create trips idx_one_agent_group_chat.
func TestResolveGroupChannelConcurrentlyBindsOneSession(t *testing.T) {
	reg, db, groupID := newSQLBackedRegistry(t, "grp-concurrent")
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), groupID), "agent1")

	channels := []Channel{"group:telegram-9", "group:feishu-3", "group:qq-1", "group:web"}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		ids   = map[string]struct{}{}
		errCh = make(chan error, len(channels))
		start = make(chan struct{})
	)
	wg.Add(len(channels))
	for _, ch := range channels {
		go func() {
			defer wg.Done()
			<-start
			info, err := reg.ResolveChatChannel(ctx, ChannelRequest{
				UserID: groupID, AgentID: "agent1", GroupID: groupID, Channel: ch,
			})
			if err != nil {
				errCh <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids[info.ID] = struct{}{}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent group resolve: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent resolves produced %d sessions, want 1", len(ids))
	}
	if n := activeGroupChatRows(t, db, groupID); n != 1 {
		t.Fatalf("%d active group chat rows, want 1", n)
	}
}

// TestOneActiveGroupChatIndexRejectsDuplicate pins the database-level backstop
// the lock fix leans on. A key mismatch is not the only way two active group
// sessions could appear (a second process, a future code path), and a group with
// two active chat rows silently splits its history in half.
func TestOneActiveGroupChatIndexRejectsDuplicate(t *testing.T) {
	_, db, groupID := newSQLBackedRegistry(t, "grp-index")
	ctx := context.Background()

	insert := func(sessionID string) error {
		_, err := db.Exec(ctx,
			`INSERT INTO ctx_conversation (session_id, agent_id, user_id, group_id, kind, channel)
			 VALUES ($1, 'agent1', $2, $3::uuid, 'chat', 'group:x')`, sessionID, groupID, groupID)
		return err
	}
	if err := insert("g-1"); err != nil {
		t.Fatalf("first active group chat row: %v", err)
	}
	if err := insert("g-2"); err == nil {
		t.Fatal("a second active group chat row was accepted; idx_one_agent_group_chat is not enforcing")
	}

	// Archiving the first frees the slot: rotation must stay possible.
	if _, err := db.Exec(ctx, `UPDATE ctx_conversation SET archived = true WHERE session_id = 'g-1'`); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := insert("g-2"); err != nil {
		t.Fatalf("successor after archive: %v", err)
	}
}

// TestResolveGroupChannelPersistsAdoptedGroupBinding is F5. A row written before
// the group binding existed carries no group_id, and bindChannelInfo only fills
// that in on the in-memory copy. Left unpersisted, the row stays anonymous
// forever: every turn re-adopts it, and nothing that reads ctx_conversation
// directly — group listings, the group event-log join — can tell whose it is.
func TestResolveGroupChannelPersistsAdoptedGroupBinding(t *testing.T) {
	reg, db, groupID := newSQLBackedRegistry(t, "grp-legacy")
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), groupID), "agent1")

	// A legacy row: right owner and kind, but no group_id and no channel.
	if _, err := db.Exec(ctx,
		`INSERT INTO ctx_conversation (session_id, agent_id, user_id, kind, channel, last_active)
		 VALUES ('legacy-group', 'agent1', $1, 'chat', '', $2)`, groupID, time.Now().UTC()); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	info, err := reg.ResolveChatChannel(ctx, ChannelRequest{
		UserID: groupID, AgentID: "agent1", GroupID: groupID, Channel: Channel("group:telegram-9"),
	})
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	if info.ID != "legacy-group" {
		t.Fatalf("resolved %q, want the adopted legacy row", info.ID)
	}

	var storedGroup, storedChannel *string
	if err := db.QueryRow(ctx,
		`SELECT group_id::text, channel FROM ctx_conversation WHERE session_id = 'legacy-group'`).
		Scan(&storedGroup, &storedChannel); err != nil {
		t.Fatalf("read adopted row: %v", err)
	}
	if storedGroup == nil || *storedGroup != groupID {
		t.Fatalf("stored group_id = %v, want %q — the adoption never reached the database", storedGroup, groupID)
	}
	if storedChannel == nil || *storedChannel != "group:telegram-9" {
		t.Fatalf("stored channel = %v, want the binding's channel", storedChannel)
	}
}
