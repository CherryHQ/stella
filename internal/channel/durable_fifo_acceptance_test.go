package channel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/platform/blob/blobtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/sessionmedia"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// acceptanceServiceManager gives ResolveWithChannel a real service identity;
// the help intent exits before the test would need a model runtime.
type acceptanceServiceManager struct{ service *agent.Service }

func (m acceptanceServiceManager) GetService(string) *agent.Service { return m.service }
func (m acceptanceServiceManager) Default() *agent.Service          { return m.service }

type acceptanceIntentClassifier struct{}

func (acceptanceIntentClassifier) Classify(context.Context, string, []ai.ContentBlock) Intent {
	return IntentHelp
}

type acceptanceRuntimeManager struct{ service *agent.Service }

func (m acceptanceRuntimeManager) GetService(string) sessionaccess.RuntimeService { return m.service }
func (m acceptanceRuntimeManager) Default() sessionaccess.RuntimeService          { return m.service }

type acceptanceChatRunner struct{}

func (*acceptanceChatRunner) Chat(context.Context, []ai.Message, agentruntime.MessageContent) <-chan agentruntime.Event {
	out := make(chan agentruntime.Event, 1)
	out <- agentruntime.Event{Text: "runtime-reply"}
	close(out)
	return out
}
func (*acceptanceChatRunner) Alive() bool             { return true }
func (*acceptanceChatRunner) Busy() bool              { return false }
func (*acceptanceChatRunner) LastActivity() time.Time { return time.Now() }
func (*acceptanceChatRunner) SystemPrompt() string    { return "" }
func (*acceptanceChatRunner) PluginContext() agentruntime.PluginContext {
	return agentruntime.PluginContext{}
}
func (*acceptanceChatRunner) Close() error { return nil }

func newDurableAcceptanceCoordinator(t *testing.T) (*Coordinator, *pgxpool.Pool, string) {
	t.Helper()
	ts := setupStores(t)
	ctx := context.Background()
	user := createTestUser(t, ts.oidcStore, "durable-acceptance@example.test")
	agentID := ts.stellaAgentID(t)
	if err := ts.authStore.AssignAgent(ctx, user.ID, agentID); err != nil {
		t.Fatalf("assign agent: %v", err)
	}
	createTestIdentity(t, ts.oidcStore, user.ID, "telegram", "sender-1", user.Name)
	// The durable binding references the configured channel row. An unbound
	// channel keeps ordinary user assignment resolution in this acceptance path.
	if err := ts.store.CreateChannel(ctx, config.Channel{ID: "telegram", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	c := &Coordinator{
		serviceManager:   acceptanceServiceManager{service: &agent.Service{AgentID: agentID}},
		store:            ts.store,
		auth:             ts.authStore,
		agentAccess:      access.NewService(ts.store, ts.authStore),
		intentClassifier: acceptanceIntentClassifier{},
	}
	return c, ts.db, user.ID
}

func newDurableRuntimeAcceptanceCoordinator(t *testing.T) (*Coordinator, *pgxpool.Pool, *agentrun.Store, string) {
	t.Helper()
	ts := setupStores(t)
	ctx := context.Background()
	user := createTestUser(t, ts.oidcStore, "durable-runtime@example.test")
	agentID := ts.stellaAgentID(t)
	if err := ts.authStore.AssignAgent(ctx, user.ID, agentID); err != nil {
		t.Fatalf("assign agent: %v", err)
	}
	createTestIdentity(t, ts.oidcStore, user.ID, "telegram", "runtime-sender", user.Name)
	if err := ts.store.CreateChannel(ctx, config.Channel{ID: "telegram", Type: "telegram", Enabled: true, Config: `{}`}); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	runs := agentrun.NewStoreWithLease(ctx, ts.db, uuid.NewString(), time.Minute)
	t.Cleanup(runs.Close)
	if err := runs.RegisterBoot(ctx); err != nil {
		t.Fatalf("register runtime boot: %v", err)
	}
	mem, err := lcm.New(ts.db, nil, nil)
	if err != nil {
		t.Fatalf("new LCM memory provider: %v", err)
	}
	sessionID := uuid.NewString()
	now := time.Now().UTC()
	mainChannel := agent.BuildUserSessionKey(agentID, user.ID, "private")
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: sessionID, UserID: user.ID, AgentID: agentID, Channel: mainChannel, Kind: string(session.KindMain), CreatedAt: now, LastActive: now}); err != nil {
		t.Fatalf("seed main session memory: %v", err)
	}
	blobs, err := blobtest.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatalf("blobtest.NewFSStore: %v", err)
	}
	assets, err := asset.NewStore(t.TempDir(), blobs, nil)
	if err != nil {
		t.Fatalf("asset.NewStore: %v", err)
	}
	agentAccess := access.NewService(ts.store, ts.authStore)
	sessionSvc, err := sessionaccess.NewService(mem, ts.db, ts.store, assets, agentAccess)
	if err != nil {
		t.Fatalf("session access service: %v", err)
	}
	rt, err := agentruntime.New(agentruntime.Config{
		AgentRuns: runs,
		Memory:    mem,
		NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
			return &acceptanceChatRunner{}, nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	svc := &agent.Service{AgentID: agentID, Runtime: rt}
	svc.SessionAccess = sessionaccess.NewAgentSessionAccess(sessionSvc)
	if err := sessionSvc.BindRuntimeManager(acceptanceRuntimeManager{service: svc}); err != nil {
		t.Fatalf("bind runtime manager: %v", err)
	}
	c := &Coordinator{
		serviceManager: acceptanceServiceManager{service: svc},
		store:          ts.store,
		auth:           ts.authStore,
		agentAccess:    agentAccess,
		queue:          newSessionQueue(),
	}
	return c, ts.db, runs, user.ID
}

func TestDurableIngressAcceptanceDirectAdmitProcessOneAck(t *testing.T) {
	c, db, _ := newDurableAcceptanceCoordinator(t)
	d := NewDurableIngress(db, "acceptance-direct")
	d.BindCoordinator(c)
	ctx := context.Background()
	streamText, handled, stream, err := d.Admit(ctx, pkgchannel.IncomingMessage{
		MessageID: "direct-1", Platform: "telegram", ChannelID: "telegram", SenderID: "sender-1",
		ChatID: "chat-1", Content: []ai.ContentBlock{ai.TextContent{Text: "help"}},
	}, "", "")
	if err != nil || handled || stream == nil || streamText != "" {
		t.Fatalf("Admit = text:%q handled:%v stream:%v err:%v", streamText, handled, stream != nil, err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- d.processOne(ctx) }()
	select {
	case event := <-stream.Events:
		if event.Text != pkgchannel.WelcomeMessage {
			t.Fatalf("event text = %q, want welcome", event.Text)
		}
	case err := <-processDone:
		var state, source, code, detail string
		_ = db.QueryRow(ctx, `SELECT state, source_key, error_code, error_detail FROM channel_fifo_item ORDER BY created_at DESC LIMIT 1`).Scan(&state, &source, &code, &detail)
		t.Fatalf("durable consumer exited before reply: %v (state=%q source=%q code=%q detail=%q)", err, state, source, code, detail)
	case <-time.After(time.Second):
		t.Fatal("durable consumer did not produce a reply")
	}
	if err := stream.Completion.Ack(ctx, pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("adapter Ack: %v", err)
	}
	select {
	case err := <-processDone:
		if err != nil {
			t.Fatalf("processOne: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("durable consumer did not settle")
	}
	var state string
	if err := db.QueryRow(ctx, `SELECT state FROM channel_fifo_item WHERE source_key = 'message:direct-1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("FIFO state = %q, want completed", state)
	}
}

func TestDurableIngressAcceptanceRealRunLinkAndCompletionAck(t *testing.T) {
	c, db, _, _ := newDurableRuntimeAcceptanceCoordinator(t)
	d := NewDurableIngress(db, "acceptance-runtime")
	d.BindCoordinator(c)
	ctx := context.Background()
	_, handled, stream, err := d.Admit(ctx, pkgchannel.IncomingMessage{
		MessageID: "runtime-1", Platform: "telegram", ChannelID: "telegram", SenderID: "runtime-sender",
		ChatID: "runtime-chat", Content: []ai.ContentBlock{ai.TextContent{Text: "ordinary turn"}},
	}, "", "")
	if err != nil || handled || stream == nil {
		t.Fatalf("Admit = handled:%v stream:%v err:%v", handled, stream != nil, err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- d.processOne(ctx) }()
	select {
	case event := <-stream.Events:
		if event.Text != "runtime-reply" {
			t.Fatalf("event text = %q, want runtime reply", event.Text)
		}
	case err := <-processDone:
		t.Fatalf("consumer exited before runtime reply: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime-backed consumer did not produce a reply")
	}

	q := sqlc.New(db)
	var item sqlc.ChannelFifoItem
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		candidate, getErr := q.GetChannelFIFOItemBySource(ctx, sqlc.GetChannelFIFOItemBySourceParams{BindingID: mustBindingID(t, db, "runtime-chat"), SourceKey: "message:runtime-1"})
		if getErr == nil && candidate.RunID.Valid {
			item = candidate
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !item.RunID.Valid {
		t.Fatal("FIFO item never linked to an AgentRun")
	}
	if item.State != "running" {
		t.Fatalf("FIFO state before adapter Ack = %q, want running", item.State)
	}
	run := waitAgentRunReady(t, q, item.RunID.String)
	if run.Status != "running" || run.CompletionState != "ready" {
		t.Fatalf("run before adapter Ack = status:%q completion:%q, want running/ready", run.Status, run.CompletionState)
	}
	if err := stream.Completion.Ack(ctx, pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("adapter Ack: %v", err)
	}
	select {
	case err := <-processDone:
		if err != nil {
			t.Fatalf("processOne: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consumer did not settle after adapter Ack")
	}
	completed, err := q.GetChannelFIFOItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "completed" || !completed.ReleasedAt.Valid || !completed.RunID.Valid {
		t.Fatalf("FIFO after Ack = state:%q released:%v run:%v, want terminal with link", completed.State, completed.ReleasedAt.Valid, completed.RunID.Valid)
	}
	run, err = q.GetAgentRun(ctx, item.RunID.String)
	if err != nil {
		t.Fatal(err)
	}
	if run.CompletionState != "acked" || run.CompletionOutcome != string(pkgchannel.EgressDelivered) {
		t.Fatalf("run after Ack = completion:%q outcome:%q, want acked/delivered", run.CompletionState, run.CompletionOutcome)
	}
	var accepted, released int64
	if err := db.QueryRow(ctx, `SELECT accepted_bytes, released_bytes FROM channel_deployment_quota WHERE channel_id = 'deployment'`).Scan(&accepted, &released); err != nil {
		t.Fatal(err)
	}
	if accepted != released || accepted == 0 {
		t.Fatalf("deployment quota accepted=%d released=%d, want equal and positive", accepted, released)
	}
}

func TestDurableIngressAcceptanceTextNewTextRealRunsAndDuplicateBarrier(t *testing.T) {
	c, db, _, _ := newDurableRuntimeAcceptanceCoordinator(t)
	d := NewDurableIngress(db, "acceptance-runtime-sequence")
	d.BindCoordinator(c)
	ctx := context.Background()
	firstItem, firstRun := admitProcessRuntimeText(t, d, db, "sequence-text-1", "first")
	firstSession := firstRun.SessionID

	newMsg := pkgchannel.IncomingMessage{
		MessageID: "sequence-new", Platform: "telegram", ChannelID: "telegram", SenderID: "runtime-sender",
		ChatID: "runtime-chat", Content: []ai.ContentBlock{ai.TextContent{Text: "/new"}},
	}
	_, handled, stream, err := d.Admit(ctx, newMsg, newSessionCommand, "")
	if err != nil || handled || stream == nil {
		t.Fatalf("/new Admit = handled:%v stream:%v err:%v", handled, stream != nil, err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- d.processOne(ctx) }()
	select {
	case event := <-stream.Events:
		if event.Text != pkgchannel.NewSessionStartedMessage {
			t.Fatalf("/new reply = %q, want started", event.Text)
		}
	case err := <-processDone:
		t.Fatalf("/new consumer exited before reply: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("/new consumer did not produce a reply")
	}
	if err := stream.Completion.Ack(ctx, pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("/new Ack: %v", err)
	}
	select {
	case err := <-processDone:
		if err != nil {
			t.Fatalf("/new processOne: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/new consumer did not settle")
	}
	rc, err := c.resolve(ctx, newMsg)
	if err != nil {
		t.Fatalf("resolve successor: %v", err)
	}
	successor, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("resolve successor session: %v", err)
	}
	if successor.ID == firstSession {
		t.Fatalf("/new kept session %s", firstSession)
	}

	// A platform redelivery of the same destructive command must consume the
	// existing receipt and leave the successor alone.
	beforeDuplicate := successor.ID
	plain, handled, duplicate, err := d.Admit(ctx, newMsg, newSessionCommand, "")
	if err != nil || !handled || duplicate != nil || plain != pkgchannel.SessionAlreadyResetMessage {
		t.Fatalf("duplicate /new = text:%q handled:%v stream:%v err:%v", plain, handled, duplicate != nil, err)
	}
	afterDuplicate, err := c.resolve(ctx, newMsg)
	if err != nil {
		t.Fatalf("resolve after duplicate: %v", err)
	}
	current, err := afterDuplicate.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("resolve current after duplicate: %v", err)
	}
	if current.ID != beforeDuplicate {
		t.Fatalf("duplicate /new rotated %s to %s", beforeDuplicate, current.ID)
	}

	secondItem, secondRun := admitProcessRuntimeText(t, d, db, "sequence-text-2", "second")
	if secondRun.SessionID == firstSession {
		t.Fatalf("second text run %s stayed on pre-/new session %s", secondRun.ID, firstSession)
	}
	if secondRun.SessionID != current.ID {
		t.Fatalf("second text run session = %s, want successor %s", secondRun.SessionID, current.ID)
	}
	if firstItem.ID == secondItem.ID || firstRun.ID == secondRun.ID {
		t.Fatal("text turns reused a FIFO item or AgentRun")
	}
}

func admitProcessRuntimeText(t *testing.T, d *DurableIngress, db *pgxpool.Pool, messageID, text string) (sqlc.ChannelFifoItem, sqlc.AgentRun) {
	t.Helper()
	ctx := context.Background()
	_, handled, stream, err := d.Admit(ctx, pkgchannel.IncomingMessage{
		MessageID: messageID, Platform: "telegram", ChannelID: "telegram", SenderID: "runtime-sender",
		ChatID: "runtime-chat", Content: []ai.ContentBlock{ai.TextContent{Text: text}},
	}, "", "")
	if err != nil || handled || stream == nil {
		t.Fatalf("text %q Admit = handled:%v stream:%v err:%v", text, handled, stream != nil, err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- d.processOne(ctx) }()
	select {
	case event := <-stream.Events:
		if event.Text != "runtime-reply" {
			t.Fatalf("text %q reply = %q", text, event.Text)
		}
	case err := <-processDone:
		t.Fatalf("text %q consumer exited before reply: %v", text, err)
	case <-time.After(2 * time.Second):
		t.Fatalf("text %q consumer did not produce a reply", text)
	}
	bindingID := mustBindingID(t, db, "runtime-chat")
	q := sqlc.New(db)
	var item sqlc.ChannelFifoItem
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		candidate, getErr := q.GetChannelFIFOItemBySource(ctx, sqlc.GetChannelFIFOItemBySourceParams{BindingID: bindingID, SourceKey: "message:" + messageID})
		if getErr == nil && candidate.RunID.Valid {
			item = candidate
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !item.RunID.Valid {
		t.Fatalf("text %q FIFO item never linked to an AgentRun", text)
	}
	run := waitAgentRunReady(t, q, item.RunID.String)
	if run.Status != "running" || run.CompletionState != "ready" {
		t.Fatalf("text %q run before Ack = status:%q completion:%q, want running/ready", text, run.Status, run.CompletionState)
	}
	if err := stream.Completion.Ack(ctx, pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("text %q Ack: %v", text, err)
	}
	select {
	case err := <-processDone:
		if err != nil {
			t.Fatalf("text %q processOne: %v", text, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("text %q consumer did not settle", text)
	}
	run, err = q.GetAgentRun(ctx, item.RunID.String)
	if err != nil {
		t.Fatal(err)
	}
	if run.CompletionState != "acked" || run.CompletionOutcome != string(pkgchannel.EgressDelivered) {
		t.Fatalf("text %q run after Ack = completion:%q outcome:%q", text, run.CompletionState, run.CompletionOutcome)
	}
	return item, run
}

func waitAgentRunReady(t *testing.T, q *sqlc.Queries, qid string) sqlc.AgentRun {
	// Runtime emits the final model event before it commits the guarded
	// completion transition. Wait for ready while asserting the source lease is
	// still running; the adapter Ack is the only operation allowed to terminalize it.
	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := q.GetAgentRun(ctx, qid)
		if err != nil {
			t.Fatalf("read AgentRun %s: %v", qid, err)
		}
		if run.Status != "running" {
			t.Fatalf("AgentRun %s left running state before adapter Ack: %q", qid, run.Status)
		}
		if run.CompletionState == "ready" {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("AgentRun %s did not reach ready before adapter Ack", qid)
	return sqlc.AgentRun{}
}

func mustBindingID(t *testing.T, db *pgxpool.Pool, chatKey string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `SELECT id::text FROM channel_binding WHERE channel_id = 'telegram' AND platform = 'telegram' AND chat_key = $1`, chatKey).Scan(&id); err != nil {
		t.Fatalf("find binding %q: %v", chatKey, err)
	}
	return id
}

// TestDurableIngressAcceptanceLeaseRecovery exercises the process-independent
// claim boundary. An expired unlinked lease is made pending and is claimable
// exactly once again, retaining the same FIFO item identity.
func TestDurableIngressAcceptanceLeaseRecovery(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	ctx := context.Background()
	d := &DurableIngress{db: fx.db, owner: "lease-acceptance", lease: time.Second}
	payload, err := json.Marshal(durableIngressPayload{Message: pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "ch-1", SenderID: "sender-1"}, Content: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	item, inserted, err := d.enqueue(ctx, uuid.NewString(), "stable-source", "ch-1", "telegram", "chat-1", "", "principal-1", payload, nil, sessionmedia.Owner{}, nil, "", "", "", "", nil)
	if err != nil || !inserted {
		t.Fatalf("enqueue = %s, %v, %v", item.ID, inserted, err)
	}
	claimed, err := sqlc.New(fx.db).ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{LeaseOwner: d.owner, LeaseSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE channel_fifo_item SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.reap(ctx); err != nil {
		t.Fatal(err)
	}
	resumed, err := sqlc.New(fx.db).ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{LeaseOwner: "resumed", LeaseSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != claimed.ID || resumed.Attempt != claimed.Attempt+1 {
		t.Fatalf("resumed item = %s attempt %d, want %s attempt %d", resumed.ID, resumed.Attempt, claimed.ID, claimed.Attempt+1)
	}
}

func TestDurableIngressAcceptanceSourceDedupDoesNotReserveTwice(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	ctx := context.Background()
	d := &DurableIngress{db: fx.db, owner: "dedup-acceptance"}
	payload := json.RawMessage(`{"message":{"platform":"telegram"},"content":[]}`)
	first, inserted, err := d.enqueue(ctx, uuid.NewString(), "same-source", "ch-1", "telegram", "chat-2", "", "principal-2", payload, nil, sessionmedia.Owner{}, nil, "", "", "", "", nil)
	if err != nil || !inserted {
		t.Fatalf("first enqueue = %s, %v, %v", first.ID, inserted, err)
	}
	second, inserted, err := d.enqueue(ctx, uuid.NewString(), "same-source", "ch-1", "telegram", "chat-2", "", "principal-2", payload, nil, sessionmedia.Owner{}, nil, "", "", "", "", nil)
	if err != nil || inserted || second.ID != first.ID {
		t.Fatalf("duplicate enqueue = %s, %v, %v; want same item and no insert", second.ID, inserted, err)
	}
	var accepted int64
	if err := fx.db.QueryRow(ctx, `SELECT accepted_rows FROM channel_binding WHERE id = $1`, first.BindingID).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted != 1 {
		t.Fatalf("binding accepted_rows = %d, want 1", accepted)
	}
}

func TestDurableIngressAcceptanceTextNewTextSharesBindingOrder(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	ctx := context.Background()
	d := &DurableIngress{db: fx.db, owner: "new-order-acceptance"}
	payload := json.RawMessage(`{"message":{"platform":"telegram"},"content":[]}`)
	commands := []string{"chat", newSessionCommand, "chat"}
	items := make([]sqlc.ChannelFifoItem, 0, len(commands))
	for i, command := range commands {
		item, inserted, err := d.enqueue(ctx, uuid.NewString(), "text-new-text:"+string(rune('1'+i)), "ch-1", "telegram", "same-chat", "", "principal-order", payload, nil, sessionmedia.Owner{}, nil, "", command, "", "", nil)
		if err != nil || !inserted {
			t.Fatalf("enqueue %q = %s, %v, %v", command, item.ID, inserted, err)
		}
		items = append(items, item)
	}
	for i, want := range items {
		claimed, err := sqlc.New(fx.db).ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{LeaseOwner: d.owner, LeaseSeconds: 60})
		if err != nil {
			t.Fatal(err)
		}
		if claimed.ID != want.ID || claimed.Seq != int64(i+1) {
			t.Fatalf("claim %d = %s seq %d, want %s seq %d", i, claimed.ID, claimed.Seq, want.ID, i+1)
		}
		if err := d.complete(ctx, claimed, "", true); err != nil {
			t.Fatalf("complete %d: %v", i, err)
		}
	}
}

func TestDurableIngressAcceptanceMediaRootUntilTerminalRelease(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	ctx := context.Background()
	q := sqlc.New(fx.db)
	ownerID := uuid.MustParse(fx.groupID)
	media, err := q.CreateMediaIfAbsent(ctx, sqlc.CreateMediaIfAbsentParams{
		GroupID: pgtype.Text{String: ownerID.String(), Valid: true}, Sha256: make([]byte, 32), MimeType: "image/jpeg", SizeBytes: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_media SET created_at = now() - interval '25 hours' WHERE id = $1`, media.ID); err != nil {
		t.Fatal(err)
	}
	d := &DurableIngress{db: fx.db, owner: "media-acceptance"}
	payload := json.RawMessage(`{"message":{"platform":"telegram"},"content":[]}`)
	item, inserted, err := d.enqueue(ctx, uuid.NewString(), "media-source", "ch-1", "telegram", "media-chat", "", "principal-media", payload,
		[]ai.ContentBlock{ai.ImageRefContent{MediaID: media.ID}}, sessionmedia.GroupOwner(ownerID), nil, "", "", "", "", nil)
	if err != nil || !inserted {
		t.Fatalf("enqueue media = %s, %v, %v", item.ID, inserted, err)
	}
	refs, err := q.ListChannelFIFOMedia(ctx, item.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("FIFO media refs = %d, err %v; want one", len(refs), err)
	}
	orphans, err := q.DeleteOrphanMedia(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("sweeper deleted %d media rows while FIFO item was live", len(orphans))
	}
	claimed, err := q.ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{LeaseOwner: d.owner, LeaseSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.complete(ctx, claimed, "", false); err != nil {
		t.Fatalf("complete media item: %v", err)
	}
	refs, err = q.ListChannelFIFOMedia(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("FIFO media refs after terminal release = %d, want zero", len(refs))
	}
	orphans, err = q.DeleteOrphanMedia(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 {
		t.Fatalf("sweeper rows after terminal release = %d, want one", len(orphans))
	}
}
