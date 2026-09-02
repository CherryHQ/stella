package channel

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/eventlog"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// authorizeAllAgentStore authorizes any agent id as a usable system agent. Paired
// with a real pool whose agent table lacks that id, it lets a test drive Create
// past authorization straight into a membership foreign-key failure — the
// mid-transaction member-write failure the rollback guarantee must survive.
type authorizeAllAgentStore struct{}

func (authorizeAllAgentStore) GetAgent(_ context.Context, id string) (config.Agent, error) {
	return config.Agent{ID: id, Name: id, Model: "anthropic/m", Scope: config.AgentScopeSystem, Enabled: true}, nil
}

func (authorizeAllAgentStore) ListAgents(context.Context) ([]config.Agent, error) { return nil, nil }

type noAssignStore struct{}

func (noAssignStore) ListUserAgentIDs(context.Context, string) ([]string, error) { return nil, nil }

// fakeDispatchRunner records wake signals without running a real agent turn.
type fakeDispatchRunner struct {
	calls int
}

type fakeGroupOwnerDeletion struct{ db *pgxpool.Pool }

func (f fakeGroupOwnerDeletion) DeleteGroup(ctx context.Context, id, _ string) error {
	return sqlc.New(f.db).DeleteGroupState(ctx, id)
}

func (f *fakeDispatchRunner) Wake() {
	f.calls++
}

func (f *fakeDispatchRunner) AbortGroupTurn(_, _ string) bool { return false }

type groupFixture struct {
	svc    *GroupService
	ts     testStores
	runner *fakeDispatchRunner
	stella string
}

func setupGroupFixture(t *testing.T) groupFixture {
	t.Helper()
	ts := setupStores(t)
	access := agentaccess.NewService(ts.store, ts.authStore)
	runner := &fakeDispatchRunner{}
	svc := NewGroupService(ts.db, access, NewRuntimeResolver(ts.store), eventlog.NewStore(ts.db), runner, WithOwnerDeletion(fakeGroupOwnerDeletion{db: ts.db}))
	return groupFixture{svc: svc, ts: ts, runner: runner, stella: ts.stellaAgentID(t)}
}

func groupUserAuthority(t *testing.T, userID string, admin bool) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID(userID), admin)
	if err != nil {
		t.Fatalf("NewUserAuthority: %v", err)
	}
	return a
}

func (fx groupFixture) begin(t *testing.T, userID string, admin bool) *GroupAccess {
	t.Helper()
	acc, err := fx.svc.Begin(fx.ts.ctx(), groupUserAuthority(t, userID, admin))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return acc
}

func (fx groupFixture) createSystemAgent(t *testing.T, id string) {
	t.Helper()
	if err := fx.ts.store.CreateAgent(fx.ts.ctx(), config.Agent{
		ID:        id,
		Name:      id,
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/" + id,
		Scope:     config.AgentScopeSystem,
		Enabled:   true,
	}); err != nil {
		t.Fatalf("CreateAgent(%q): %v", id, err)
	}
}

// TestGroupOwnerVisibilityIsOpaque proves owner/admin visibility: the owner and
// an admin reach a group, while a foreign non-admin sees it as absent (not
// forbidden), and the foreign caller's own list excludes it.
func TestGroupOwnerVisibilityIsOpaque(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	owner := createTestUser(t, fx.ts.oidcStore, "owner@example.com")
	foreign := createTestUser(t, fx.ts.oidcStore, "foreign@example.com")

	ownerAcc := fx.begin(t, owner.ID, false)
	g, err := ownerAcc.Create(ctx, "team", []string{fx.stella})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g.CreatedByUserID == nil || *g.CreatedByUserID != owner.ID {
		t.Fatalf("created_by = %v, want %q", g.CreatedByUserID, owner.ID)
	}

	if _, err := ownerAcc.Get(ctx, g.ID); err != nil {
		t.Fatalf("owner Get: %v", err)
	}
	admin := fx.begin(t, foreign.ID, true)
	if _, err := admin.Get(ctx, g.ID); err != nil {
		t.Fatalf("admin Get: %v", err)
	}

	foreignAcc := fx.begin(t, foreign.ID, false)
	for name, call := range map[string]func() error{
		"Get":          func() error { _, err := foreignAcc.Get(ctx, g.ID); return err },
		"UpdateName":   func() error { _, err := foreignAcc.UpdateName(ctx, g.ID, "x"); return err },
		"Members":      func() error { _, err := foreignAcc.Members(ctx, g.ID); return err },
		"Delete":       func() error { return foreignAcc.Delete(ctx, g.ID) },
		"AddMember":    func() error { _, err := foreignAcc.AddMember(ctx, g.ID, fx.stella); return err },
		"RemoveMember": func() error { return foreignAcc.RemoveMember(ctx, g.ID, fx.stella) },
	} {
		if err := call(); !errors.Is(err, ErrGroupNotFound) {
			t.Fatalf("foreign %s = %v, want ErrGroupNotFound", name, err)
		}
	}

	list, err := foreignAcc.List(ctx, 0, 10)
	if err != nil {
		t.Fatalf("foreign List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("foreign List = %d groups, want 0", len(list))
	}
}

func TestGroupDeleteFailsClosedWithoutOwnerLifecycle(t *testing.T) {
	ts := setupStores(t)
	access := agentaccess.NewService(ts.store, ts.authStore)
	svc := NewGroupService(ts.db, access, NewRuntimeResolver(ts.store), eventlog.NewStore(ts.db), &fakeDispatchRunner{})
	user := createTestUser(t, ts.oidcStore, "owner@example.com")
	acc, err := svc.Begin(ts.ctx(), groupUserAuthority(t, user.ID, false))
	if err != nil {
		t.Fatal(err)
	}
	group, err := acc.Create(ts.ctx(), "team", []string{ts.stellaAgentID(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := acc.Delete(ts.ctx(), group.ID); !errors.Is(err, ErrGroupUnavailable) {
		t.Fatalf("Delete = %v, want ErrGroupUnavailable", err)
	}
	if _, err := acc.Get(ts.ctx(), group.ID); err != nil {
		t.Fatalf("raw group delete ran without lifecycle: %v", err)
	}
}

// TestGroupCreateAuthorizesEveryAgent proves Create fails closed when the caller
// cannot use one of the requested agents, and writes no group.
func TestGroupCreateAuthorizesEveryAgent(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	user := createTestUser(t, fx.ts.oidcStore, "user@example.com")

	if err := fx.ts.store.CreateAgent(ctx, config.Agent{
		ID: "secret", Name: "secret", Model: "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/secret", Scope: config.AgentScopeRestricted, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	acc := fx.begin(t, user.ID, false)
	if _, err := acc.Create(ctx, "team", []string{fx.stella, "secret"}); err == nil {
		t.Fatal("Create with unusable agent = nil, want error")
	}
	list, err := acc.List(ctx, 0, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("group persisted despite denied agent: %d groups", len(list))
	}
}

// TestGroupLastMemberInvariant proves the last agent cannot be removed.
func TestGroupLastMemberInvariant(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	user := createTestUser(t, fx.ts.oidcStore, "user@example.com")
	fx.createSystemAgent(t, "second")

	acc := fx.begin(t, user.ID, false)
	g, err := acc.Create(ctx, "team", []string{fx.stella, "second"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := acc.RemoveMember(ctx, g.ID, "second"); err != nil {
		t.Fatalf("RemoveMember(second): %v", err)
	}
	if err := acc.RemoveMember(ctx, g.ID, fx.stella); !errors.Is(err, ErrLastGroupMember) {
		t.Fatalf("RemoveMember(last) = %v, want ErrLastGroupMember", err)
	}
	members, err := acc.Members(ctx, g.ID)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("members = %d, want 1", len(members))
	}
}

// TestGroupPrepareSendDedup proves the send append is idempotent under a repeated
// client_message_id: the second call collapses onto the existing row (no new
// message, no new outbox) and reports Deduplicated. The human actor id is the
// authenticated user (invariant #308).
func TestGroupPrepareSendDedup(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	user := createTestUser(t, fx.ts.oidcStore, "user@example.com")
	acc := fx.begin(t, user.ID, false)
	g, err := acc.Create(ctx, "team", []string{fx.stella})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	first, err := acc.PrepareSend(ctx, g.ID, "hello", "client-1")
	if err != nil {
		t.Fatalf("PrepareSend first: %v", err)
	}
	if first.Command || first.Deduplicated {
		t.Fatalf("first send = %+v, want fresh dispatch", first)
	}

	second, err := acc.PrepareSend(ctx, g.ID, "hello again", "client-1")
	if err != nil {
		t.Fatalf("PrepareSend replay: %v", err)
	}
	if !second.Deduplicated {
		t.Fatalf("replay = %+v, want Deduplicated", second)
	}
	if fx.runner.calls != 1 {
		t.Fatalf("dispatcher woke %d times, want once for the fresh append only", fx.runner.calls)
	}

	msgs, err := acc.Messages(ctx, g.ID, 0, 50)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1 (dedup collapsed)", len(msgs))
	}
	if msgs[0].ActorType != "human" || msgs[0].ActorID != user.ID {
		t.Fatalf("actor = %s/%s, want human/%s", msgs[0].ActorType, msgs[0].ActorID, user.ID)
	}
	// The single message has exactly one claimable outbox (created atomically in
	// the append transaction); the replay created none.
	if _, err := sqlc.New(fx.ts.db).GetGroupOutboxByMessage(ctx, msgs[0].ID); err != nil {
		t.Fatalf("expected one outbox for the appended message: %v", err)
	}
}

// TestGroupPrepareSendCommand proves group slash commands are intercepted before
// any append: no message is written and the plain reply is returned.
func TestGroupPrepareSendCommand(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	user := createTestUser(t, fx.ts.oidcStore, "user@example.com")
	acc := fx.begin(t, user.ID, false)
	g, err := acc.Create(ctx, "team", []string{fx.stella})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	prep, err := acc.PrepareSend(ctx, g.ID, "/config please", "")
	if err != nil {
		t.Fatalf("PrepareSend command: %v", err)
	}
	if !prep.Command || prep.Reply == "" {
		t.Fatalf("command send = %+v, want Command with reply", prep)
	}
	msgs, err := acc.Messages(ctx, g.ID, 0, 50)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("command wrote %d messages, want 0", len(msgs))
	}
}

// TestGroupPrepareSendNewIsRefused proves a Web group `/new` is refused
// explicitly — a group's context is shared, so no member's chat command may
// clear it — and that the refusal never reaches the event log, so the command
// does not become part of the group's context either.
func TestGroupPrepareSendNewIsRefused(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	user := createTestUser(t, fx.ts.oidcStore, "user@example.com")
	acc := fx.begin(t, user.ID, false)
	g, err := acc.Create(ctx, "team", []string{fx.stella})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	prep, err := acc.PrepareSend(ctx, g.ID, "/new", "")
	if err != nil {
		t.Fatalf("PrepareSend /new: %v", err)
	}
	if !prep.Command || prep.Reply != pkgchannel.GroupNewSessionUnsupportedMessage {
		t.Fatalf("/new = %+v, want an intercepted command refusing the reset", prep)
	}
	msgs, err := acc.Messages(ctx, g.ID, 0, 50)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("/new wrote %d messages to the event log, want 0", len(msgs))
	}
}

func TestGroupSendWakesDispatcher(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	user := createTestUser(t, fx.ts.oidcStore, "user@example.com")
	acc := fx.begin(t, user.ID, false)
	g, err := acc.Create(ctx, "team", []string{fx.stella})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = acc.PrepareSend(ctx, g.ID, "hi", "")
	if err != nil {
		t.Fatalf("PrepareSend: %v", err)
	}
	if fx.runner.calls != 1 {
		t.Fatalf("dispatcher woke %d times, want 1", fx.runner.calls)
	}
}

// A Web composer has no native mention payload, so its text must resolve a
// member's display name exactly as platform and agent text do.
func TestGroupPrepareSendResolvesDisplayNameMention(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	user := createTestUser(t, fx.ts.oidcStore, "user@example.com")
	if err := fx.ts.store.CreateAgent(ctx, config.Agent{
		ID: "ada", Name: "Ada", Model: "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/ada", Scope: config.AgentScopeSystem, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent(Ada): %v", err)
	}
	acc := fx.begin(t, user.ID, false)
	g, err := acc.Create(ctx, "team", []string{fx.stella, "ada"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := acc.PrepareSend(ctx, g.ID, "@Ada, please review this", "mention-1"); err != nil {
		t.Fatalf("PrepareSend: %v", err)
	}

	msgs, err := acc.Messages(ctx, g.ID, 0, 50)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Messages = %#v, %v; want one message", msgs, err)
	}
	outbox, err := sqlc.New(fx.ts.db).GetGroupOutboxByMessage(ctx, msgs[0].ID)
	if err != nil {
		t.Fatalf("GetGroupOutboxByMessage: %v", err)
	}
	envelope, err := DecodeGroupOutboxEnvelope(outbox.Envelope)
	if err != nil {
		t.Fatalf("DecodeGroupOutboxEnvelope: %v", err)
	}
	want := []pkgchannel.Mention{{Raw: "@Ada", AgentID: "ada"}}
	if !reflect.DeepEqual(envelope.Mentions, want) {
		t.Fatalf("mentions = %#v, want %#v", envelope.Mentions, want)
	}
}

// TestGroupSendUnavailableWithoutDispatch proves the send path degrades to
// ErrGroupUnavailable when the event log or dispatcher is not wired, while
// leaving CRUD (Create) available.
func TestGroupSendUnavailableWithoutDispatch(t *testing.T) {
	ts := setupStores(t)
	access := agentaccess.NewService(ts.store, ts.authStore)
	svc := NewGroupService(ts.db, access, NewRuntimeResolver(ts.store), nil, nil)
	ctx := ts.ctx()
	user := createTestUser(t, ts.oidcStore, "user@example.com")
	acc, err := svc.Begin(ctx, groupUserAuthority(t, user.ID, false))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	g, err := acc.Create(ctx, "team", []string{ts.stellaAgentID(t)})
	if err != nil {
		t.Fatalf("Create (CRUD must stay available): %v", err)
	}
	if _, err := acc.PrepareSend(ctx, g.ID, "hi", ""); !errors.Is(err, ErrGroupUnavailable) {
		t.Fatalf("PrepareSend without dispatch = %v, want ErrGroupUnavailable", err)
	}
}

// TestGroupCreateRollsBackOnMemberWriteFailure proves Create is atomic: when a
// membership (and its web channel) write fails partway through, the already-
// inserted group state and web channel roll back, leaving no half-provisioned
// group. The failure is a real agent_id foreign-key violation — an agent that
// authorization accepts but that is absent from the durable agent table.
func TestGroupCreateRollsBackOnMemberWriteFailure(t *testing.T) {
	ts := setupStores(t)
	agents := agentaccess.NewService(authorizeAllAgentStore{}, noAssignStore{})
	svc := NewGroupService(ts.db, agents, NewRuntimeResolver(ts.store), nil, nil)
	ctx := ts.ctx()
	owner := createTestUser(t, ts.oidcStore, "owner@example.com")

	acc, err := svc.Begin(ctx, groupUserAuthority(t, owner.ID, false))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := acc.Create(ctx, "team", []string{"ghost-agent"}); err == nil {
		t.Fatal("Create with an unpersisted agent = nil, want a member-write failure")
	}

	var groups int
	if err := ts.db.QueryRow(ctx, "SELECT COUNT(*) FROM ctx_group_state WHERE created_by_user_id = $1", owner.ID).Scan(&groups); err != nil {
		t.Fatalf("count groups: %v", err)
	}
	if groups != 0 {
		t.Fatalf("group rows after rollback = %d, want 0 (group must roll back with the failed member)", groups)
	}
	var channels int
	if err := ts.db.QueryRow(ctx, "SELECT COUNT(*) FROM channel WHERE id = $1", webChannelID("ghost-agent")).Scan(&channels); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if channels != 0 {
		t.Fatalf("web channel rows after rollback = %d, want 0 (channel write must roll back too)", channels)
	}
}

// TestGroupRemoveMemberConcurrentLastTwo proves RemoveMember serializes on the
// group row: two callers each removing one of the last two members cannot both
// succeed. Exactly one deletion commits and the other observes the last-member
// invariant, leaving exactly one member.
func TestGroupRemoveMemberConcurrentLastTwo(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	user := createTestUser(t, fx.ts.oidcStore, "user@example.com")
	fx.createSystemAgent(t, "second")
	acc := fx.begin(t, user.ID, false)
	g, err := acc.Create(ctx, "team", []string{fx.stella, "second"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Distinct access handles built on the test goroutine (Begin fatals, which is
	// illegal off the test goroutine); the removals themselves race.
	acc1 := fx.begin(t, user.ID, false)
	acc2 := fx.begin(t, user.ID, false)
	targets := []string{fx.stella, "second"}
	accs := []*GroupAccess{acc1, acc2}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range targets {
		go func(i int) {
			defer wg.Done()
			errs[i] = accs[i].RemoveMember(ctx, g.ID, targets[i])
		}(i)
	}
	wg.Wait()

	var committed, blocked int
	for _, e := range errs {
		switch {
		case e == nil:
			committed++
		case errors.Is(e, ErrLastGroupMember):
			blocked++
		default:
			t.Fatalf("unexpected RemoveMember error: %v", e)
		}
	}
	if committed != 1 || blocked != 1 {
		t.Fatalf("concurrent removal outcomes: committed=%d lastMember=%d, want 1/1", committed, blocked)
	}
	members, err := acc.Members(ctx, g.ID)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("members after concurrent removal = %d, want exactly 1", len(members))
	}
}

// TestGroupPaginationRejectsOutOfRange proves List and Messages reject paging
// arguments that would overflow or invert the int32 LIMIT/OFFSET columns before
// they reach the query layer.
// A stream replay must show what just happened. Ordering the window from the
// oldest end would make an active group replay its first messages forever.
func TestMessagesAfterSeqReplaysNewestWindow(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	user := createTestUser(t, fx.ts.oidcStore, "user@example.com")
	acc := fx.begin(t, user.ID, false)
	g, err := acc.Create(ctx, "team", []string{fx.stella})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	store := eventlog.NewStore(fx.ts.db)
	total := groupReplayWindow + 5
	for i := range total {
		if _, err := store.AppendToGroup(ctx, g.ID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: user.ID, Content: fmt.Sprintf("m%d", i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	rows, err := acc.MessagesAfterSeq(ctx, g.ID, 0)
	if err != nil {
		t.Fatalf("MessagesAfterSeq: %v", err)
	}
	if len(rows) != groupReplayWindow {
		t.Fatalf("replayed %d rows, want the %d-row window", len(rows), groupReplayWindow)
	}
	if rows[0].Seq >= rows[len(rows)-1].Seq {
		t.Fatalf("replay not ascending: %d..%d", rows[0].Seq, rows[len(rows)-1].Seq)
	}
	if got, want := rows[len(rows)-1].Content, fmt.Sprintf("m%d", total-1); got != want {
		t.Fatalf("last replayed = %q, want the newest message %q", got, want)
	}
}

func TestGroupPaginationRejectsOutOfRange(t *testing.T) {
	fx := setupGroupFixture(t)
	ctx := fx.ts.ctx()
	user := createTestUser(t, fx.ts.oidcStore, "user@example.com")
	acc := fx.begin(t, user.ID, false)

	cases := []struct {
		name         string
		offset, size int
	}{
		{"huge offset", math.MaxInt32 + 1, 10},
		{"huge limit", 0, math.MaxInt32 + 1},
		{"overflowing window", math.MaxInt32, 1},
		{"zero limit", 0, 0},
		{"negative offset", -1, 10},
	}
	for _, tc := range cases {
		if _, err := acc.List(ctx, tc.offset, tc.size); !errors.Is(err, ErrInvalidPage) {
			t.Fatalf("List %s = %v, want ErrInvalidPage", tc.name, err)
		}
		// Messages validates paging before any ownership read, so a nonexistent
		// group id still surfaces the pagination error, not not-found.
		if _, err := acc.Messages(ctx, "any-group", tc.offset, tc.size); !errors.Is(err, ErrInvalidPage) {
			t.Fatalf("Messages %s = %v, want ErrInvalidPage", tc.name, err)
		}
	}
}
