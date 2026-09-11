package channel

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	agentrun "github.com/CherryHQ/stella/internal/agent/run"
	"github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/asset"
	chinbox "github.com/CherryHQ/stella/internal/channel/inbox"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// setupRouter builds a coordinator for the durable path only: no
// ServiceManager, no live sessions, no publishers. That is the point of the
// phase — a replica with zero local agent services still receives and routes.
func setupRouter(t *testing.T) (*Coordinator, testStores) {
	t.Helper()
	ts := setupStores(t)
	mem, err := lcm.New(ts.db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentAccess := agentaccess.NewService(ts.store, ts.authStore, agentaccess.WithGuestPolicyDecoder(fixedGuestPolicy))
	sessSvc, err := access.NewService(mem, ts.db, ts.store, assets, agentAccess)
	if err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{
		db:            ts.db,
		store:         ts.store,
		auth:          ts.authStore,
		agentAccess:   agentAccess,
		sessionAccess: access.NewAgentSessionAccess(sessSvc),
		queue:         newSessionQueue(),
		guestLimiter:  newGuestRateLimiter(),
		guests:        NewGuestStore(ts.db),
		guestPolicy:   fixedGuestPolicy,
	}
	return c, ts
}

func fixedGuestPolicy(_ string, _ string) (pkgchannel.GuestConfig, error) {
	return pkgchannel.GuestConfig{
		AllowDM:                    true,
		AllowUnlinkedDM:            true,
		GuestMessageLimitPerMinute: 100,
		GuestMaxPerChannel:         50,
	}, nil
}

func textMsg(platform, channelID, senderID, messageID, text string) pkgchannel.IncomingMessage {
	return pkgchannel.IncomingMessage{
		Platform:  platform,
		ChannelID: channelID,
		SenderID:  senderID,
		MessageID: messageID,
		Content:   []ai.ContentBlock{ai.TextContent{Text: text}},
	}
}

func linkTelegramUser(t *testing.T, ts testStores, email, extID string) string {
	t.Helper()
	u := createTestUser(t, ts.oidcStore, email)
	createTestIdentity(t, ts.oidcStore, u.ID, "telegram", extID, email)
	return u.ID
}

func queryRun(t *testing.T, c *Coordinator, inboxID string) sqlc.AgentRun {
	t.Helper()
	var r sqlc.AgentRun
	err := c.db.QueryRow(context.Background(),
		`SELECT id, session_id, agent_id, request_key, state, reply_address FROM agent_run WHERE inbox_id = $1`, inboxID).
		Scan(&r.ID, &r.SessionID, &r.AgentID, &r.RequestKey, &r.State, &r.ReplyAddress)
	if err != nil {
		t.Fatalf("query run for inbox %s: %v", inboxID, err)
	}
	return r
}

func countRuns(t *testing.T, c *Coordinator) int {
	t.Helper()
	var n int
	if err := c.db.QueryRow(context.Background(), "SELECT count(*) FROM agent_run").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countOutbox(t *testing.T, c *Coordinator) int {
	t.Helper()
	var n int
	if err := c.db.QueryRow(context.Background(), "SELECT count(*) FROM channel_outbox").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func inboxState(t *testing.T, c *Coordinator, id string) string {
	t.Helper()
	var s string
	if err := c.db.QueryRow(context.Background(), "SELECT state FROM channel_inbox WHERE id = $1", id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRouterRoutesMessageWithoutLocalService(t *testing.T) {
	c, ts := setupRouter(t)
	ctx := context.Background()
	linkTelegramUser(t, ts, "route@example.com", "tg-route")
	if err := ts.store.CreateChannel(ctx, config.Channel{ID: "tg-1", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatal(err)
	}
	if c.serviceManager != nil {
		t.Fatal("router coordinator must not hold a ServiceManager")
	}

	_, handled, stream, err := c.HandleIncoming(ctx, textMsg("telegram", "tg-1", "tg-route", "m1", "hello"), "", "")
	if err != nil || !handled || stream != nil {
		t.Fatalf("receive: handled=%v stream=%v err=%v", handled, stream, err)
	}
	var inboxID string
	if err := c.db.QueryRow(ctx, "SELECT id FROM channel_inbox WHERE event_key='tg-route:m1'").Scan(&inboxID); err != nil {
		t.Fatal(err)
	}

	// Simulate the receiver dying: a different coordinator instance routes.
	c2, _ := setupRouterOn(t, ts)
	n, err := c2.RoutePending(ctx, "tg-1")
	if err != nil || n != 1 {
		t.Fatalf("RoutePending: n=%d err=%v", n, err)
	}
	if got := inboxState(t, c, inboxID); got != "routed" {
		t.Fatalf("inbox state = %s, want routed", got)
	}
	r := queryRun(t, c, inboxID)
	if r.State != "queued" {
		t.Fatalf("run state = %s, want queued", r.State)
	}
	var reply agentrun.ReplyAddress
	if err := json.Unmarshal(r.ReplyAddress, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ChannelID != "tg-1" || reply.ChatKey != "tg-route" || reply.ReplyToKey != "m1" {
		t.Fatalf("reply address = %+v", reply)
	}

	// A redelivery attaches, never duplicates.
	if _, _, _, err := c2.HandleIncoming(ctx, textMsg("telegram", "tg-1", "tg-route", "m1", "hello"), "", ""); err != nil {
		t.Fatal(err)
	}
	n, err = c2.RoutePending(ctx, "tg-1")
	if err != nil || n != 0 {
		t.Fatalf("re-route: n=%d err=%v", n, err)
	}
	if countRuns(t, c) != 1 {
		t.Fatalf("runs = %d, want 1", countRuns(t, c))
	}
}

// setupRouterOn builds a second replica's coordinator against the same DB.
func setupRouterOn(t *testing.T, ts testStores) (*Coordinator, testStores) {
	t.Helper()
	mem, err := lcm.New(ts.db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentAccess := agentaccess.NewService(ts.store, ts.authStore, agentaccess.WithGuestPolicyDecoder(fixedGuestPolicy))
	sessSvc, err := access.NewService(mem, ts.db, ts.store, assets, agentAccess)
	if err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{
		db:            ts.db,
		store:         ts.store,
		auth:          ts.authStore,
		agentAccess:   agentAccess,
		sessionAccess: access.NewAgentSessionAccess(sessSvc),
		queue:         newSessionQueue(),
		guestLimiter:  newGuestRateLimiter(),
		guests:        NewGuestStore(ts.db),
		guestPolicy:   fixedGuestPolicy,
	}
	return c, ts
}

func TestRouterNewSessionOrdering(t *testing.T) {
	c, ts := setupRouter(t)
	ctx := context.Background()
	linkTelegramUser(t, ts, "new@example.com", "tg-new")
	if err := ts.store.CreateChannel(ctx, config.Channel{ID: "tg-n", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatal(err)
	}

	mustReceive := func(id, text string) string {
		t.Helper()
		_, _, _, err := c.HandleIncoming(ctx, textMsg("telegram", "tg-n", "tg-new", id, text), "", "")
		if err != nil {
			t.Fatal(err)
		}
		var inboxID string
		if err := c.db.QueryRow(ctx, "SELECT id FROM channel_inbox WHERE event_key='tg-new:' || $1", id).Scan(&inboxID); err != nil {
			t.Fatal(err)
		}
		return inboxID
	}
	mustReceiveCmd := func(id, command string) string {
		t.Helper()
		_, _, _, err := c.HandleIncoming(ctx, pkgchannel.IncomingMessage{
			Platform: "telegram", ChannelID: "tg-n", SenderID: "tg-new", MessageID: id,
		}, command, "")
		if err != nil {
			t.Fatal(err)
		}
		var inboxID string
		if err := c.db.QueryRow(ctx, "SELECT id FROM channel_inbox WHERE event_key='tg-new:' || $1", id).Scan(&inboxID); err != nil {
			t.Fatal(err)
		}
		return inboxID
	}

	msg1 := mustReceive("msg-1", "before")
	new1 := mustReceiveCmd("new-1", "/new")
	msg2 := mustReceive("msg-2", "after")

	if n, err := c.RoutePending(ctx, "tg-n"); err != nil || n != 3 {
		t.Fatalf("RoutePending: n=%d err=%v", n, err)
	}
	r1 := queryRun(t, c, msg1)
	r2 := queryRun(t, c, msg2)
	if r1.SessionID == r2.SessionID {
		t.Fatal("post-/new message must land on the rotated session")
	}
	if inboxState(t, c, new1) != "routed" {
		t.Fatal("/new event not routed")
	}
	if countOutbox(t, c) != 1 {
		t.Fatalf("outbox ops = %d, want 1", countOutbox(t, c))
	}

	// Redelivery of the same /new message rotates nothing and re-answers.
	mustReceiveCmd("new-1", "/new")
	if n, err := c.RoutePending(ctx, "tg-n"); err != nil || n != 0 {
		t.Fatalf("redelivered /new routed again: n=%d", n)
	}
}

func TestRouterAbortFlagsExecution(t *testing.T) {
	c, ts := setupRouter(t)
	ctx := context.Background()
	linkTelegramUser(t, ts, "abort@example.com", "tg-abort")
	if err := ts.store.CreateChannel(ctx, config.Channel{ID: "tg-a", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.HandleIncoming(ctx, textMsg("telegram", "tg-a", "tg-abort", "m1", "hi"), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RoutePending(ctx, "tg-a"); err != nil {
		t.Fatal(err)
	}
	var sessionID string
	if err := c.db.QueryRow(ctx, "SELECT session_id FROM agent_run").Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	// Fake worker claims the execution lease on the session.
	q := sqlc.New(c.db)
	lease, err := q.ClaimSessionExecution(ctx, sqlc.ClaimSessionExecutionParams{
		SessionID: sessionID,
		Token:     uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("claim execution: %v", err)
	}
	if _, _, _, err := c.HandleIncoming(ctx, pkgchannel.IncomingMessage{
		Platform: "telegram", ChannelID: "tg-a", SenderID: "tg-abort", MessageID: "a1",
	}, "/abort", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RoutePending(ctx, "tg-a"); err != nil {
		t.Fatal(err)
	}
	row, err := q.GetSessionExecution(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.CancelRequested || row.Token != lease.Token {
		t.Fatalf("cancel_requested=%v", row.CancelRequested)
	}
	if countOutbox(t, c) != 1 {
		t.Fatalf("outbox ops = %d, want 1", countOutbox(t, c))
	}
}

func TestRouterUnreadyEventBlocksItsChatOnly(t *testing.T) {
	c, ts := setupRouter(t)
	ctx := context.Background()
	linkTelegramUser(t, ts, "ready@example.com", "tg-ready")
	// An unbound channel admits no unlinked sender; bind the default agent so
	// the second chat's sender resolves as a guest.
	if err := ts.store.CreateChannel(ctx, config.Channel{ID: "tg-r", Type: "telegram", AgentID: ts.stellaAgentID(t), Enabled: true, Config: `{}`}); err != nil {
		t.Fatal(err)
	}
	env1, err := chinbox.MarshalIncoming(textMsg("telegram", "tg-r", "tg-ready", "s1", "staged"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	env2, err := chinbox.MarshalIncoming(textMsg("telegram", "tg-r", "tg-other", "s2", "other chat"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	env3, err := chinbox.MarshalIncoming(textMsg("telegram", "tg-r", "tg-ready", "s3", "later"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	inbox := chinbox.New(c.db)
	// s1 stays 'received' (attachments not staged); s3 must not overtake it;
	// s2 is a different chat and routes freely.
	if _, _, err := inbox.Receive(ctx, chinbox.ReceiveParams{
		ChannelID: "tg-r", SourceAccountKey: "tg-r", EventKey: "s1",
		EventKind: chinbox.KindMessage, PayloadVersion: chinbox.EnvelopeVersion,
		Payload: env1, ChatKey: "tg-ready", Ready: false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbox.Receive(ctx, chinbox.ReceiveParams{
		ChannelID: "tg-r", SourceAccountKey: "tg-r", EventKey: "s3",
		EventKind: chinbox.KindMessage, PayloadVersion: chinbox.EnvelopeVersion,
		Payload: env3, ChatKey: "tg-ready", Ready: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbox.Receive(ctx, chinbox.ReceiveParams{
		ChannelID: "tg-r", SourceAccountKey: "tg-r", EventKey: "s2",
		EventKind: chinbox.KindMessage, PayloadVersion: chinbox.EnvelopeVersion,
		Payload: env2, ChatKey: "tg-other", Ready: true,
	}); err != nil {
		t.Fatal(err)
	}

	n, err := c.RoutePending(ctx, "tg-r")
	if err != nil || n != 1 {
		t.Fatalf("RoutePending: n=%d err=%v, want 1 (only s2)", n, err)
	}
	var states []string
	rows, err := c.db.Query(ctx, "SELECT event_key, state FROM channel_inbox ORDER BY ingress_seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, s string
		if err := rows.Scan(&k, &s); err != nil {
			t.Fatal(err)
		}
		states = append(states, k+"="+s)
	}
	want := []string{"s1=received", "s3=ready", "s2=routed"}
	if len(states) != 3 || states[0] != want[0] || states[1] != want[1] || states[2] != want[2] {
		t.Fatalf("states = %v, want %v", states, want)
	}
}

func TestRouterRejectsUnknownKind(t *testing.T) {
	c, ts := setupRouter(t)
	ctx := context.Background()
	if err := ts.store.CreateChannel(ctx, config.Channel{ID: "tg-x", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatal(err)
	}
	env, _ := chinbox.MarshalIncoming(textMsg("telegram", "tg-x", "u", "cb1", ""), "", "")
	if _, _, err := chinbox.New(c.db).Receive(ctx, chinbox.ReceiveParams{
		ChannelID: "tg-x", SourceAccountKey: "tg-x", EventKey: "cb1",
		EventKind: "callback", PayloadVersion: chinbox.EnvelopeVersion,
		Payload: env, ChatKey: "u", Ready: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RoutePending(ctx, "tg-x"); err != nil {
		t.Fatal(err)
	}
	var state, code string
	if err := c.db.QueryRow(ctx, "SELECT state, error_code FROM channel_inbox WHERE event_key='cb1'").Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "rejected" || code != "unsupported_kind" {
		t.Fatalf("state=%s code=%s", state, code)
	}
}

// fakeRunExecutor is the Phase-2/3 "假的执行消费者": it never touches a real
// agent Service, just proves the run carries enough durable facts to execute
// and that the reply lands in the outbox inside the finish transaction.
type fakeRunExecutor struct {
	reply string
	got   atomic.Int32
}

func (f *fakeRunExecutor) Execute(_ context.Context, r sqlc.AgentRun) (string, error) {
	f.got.Add(1)
	return f.reply, nil
}

func TestDurablePathEndToEnd(t *testing.T) {
	c, ts := setupRouter(t)
	ctx := context.Background()
	linkTelegramUser(t, ts, "e2e@example.com", "tg-e2e")
	if err := ts.store.CreateChannel(ctx, config.Channel{ID: "tg-e2e", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.HandleIncoming(ctx, textMsg("telegram", "tg-e2e", "tg-e2e", "m1", "question"), "", ""); err != nil {
		t.Fatal(err)
	}
	if n, err := c.RoutePending(ctx, "tg-e2e"); err != nil || n != 1 {
		t.Fatalf("route: n=%d err=%v", n, err)
	}

	exec := &fakeRunExecutor{reply: "answer"}
	w := agentrun.NewWorker(c.db, "w-test", exec, c.runFinishHook)
	ok, err := w.ProcessOnce(ctx)
	if err != nil || !ok {
		t.Fatalf("worker: ok=%v err=%v", ok, err)
	}
	if exec.got.Load() != 1 {
		t.Fatal("executor not called")
	}
	var state string
	if err := c.db.QueryRow(ctx, "SELECT state FROM agent_run").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("run state = %s", state)
	}
	var opKind string
	var payload json.RawMessage
	if err := c.db.QueryRow(ctx, "SELECT operation_kind, payload FROM channel_outbox").Scan(&opKind, &payload); err != nil {
		t.Fatal(err)
	}
	if opKind != "send_text" {
		t.Fatalf("outbox kind = %s", opKind)
	}
	var addr struct {
		ChatKey string `json:"chat_key"`
	}
	var text struct {
		Text string `json:"text"`
	}
	if err := c.db.QueryRow(ctx, "SELECT address FROM channel_outbox").Scan(&addr); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &text); err != nil || text.Text != "answer" {
		t.Fatalf("payload = %s", payload)
	}
	if addr.ChatKey != "tg-e2e" {
		t.Fatalf("address chat_key = %s", addr.ChatKey)
	}
	// Execution lease released.
	var n int
	if err := c.db.QueryRow(ctx, "SELECT count(*) FROM ctx_session_execution").Scan(&n); err != nil || n != 0 {
		t.Fatalf("execution rows left: %d", n)
	}
}
