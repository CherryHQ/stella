package profile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// fakeProfiles counts every access so a test can prove the Agent gate ran before
// any store touch.
type fakeProfiles struct {
	content, soul string
	reads, writes int
}

func (f *fakeProfiles) GetProfile(context.Context, string, string) (string, error) {
	f.reads++
	return f.content, nil
}

func (f *fakeProfiles) GetAgentSoul(context.Context, string, string) (string, error) {
	f.reads++
	return f.soul, nil
}

func (f *fakeProfiles) SetProfile(context.Context, string, string, string) error {
	f.writes++
	return nil
}

func (f *fakeProfiles) SetAgentSoul(context.Context, string, string, string) error {
	f.writes++
	return nil
}

type fakeAuthorizer struct {
	err   error
	calls int
}

func (f *fakeAuthorizer) Authorize(context.Context, authz.Authority, string, authz.Action) error {
	f.calls++
	return f.err
}

func userAuthority(t *testing.T, admin bool) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID("u1"), admin)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// fakeKnowledge records the owner tuples the boundary forwards so a test can prove
// the Agent gate runs first (calls stay 0 on denial) and that the owner id and
// actor id come from the Authority, never a caller field.
type fakeKnowledge struct {
	calls              int
	lastUserID         string
	lastDeprecatedBy   string
	lastRestoredBy     string
	lastChangelogOwner string
}

func (f *fakeKnowledge) ListKnowledge(_ context.Context, in memorywrite.KnowledgeListQuery) (memorywrite.KnowledgePage, error) {
	f.calls++
	f.lastUserID = in.UserID
	return memorywrite.KnowledgePage{}, nil
}

func (f *fakeKnowledge) CreateKnowledge(_ context.Context, in memorywrite.KnowledgeCreateInput) (memory.Fact, error) {
	f.calls++
	f.lastUserID = in.UserID
	return memory.Fact{}, nil
}

func (f *fakeKnowledge) ReplaceKnowledge(_ context.Context, in memorywrite.KnowledgeReplaceInput) (memory.Fact, error) {
	f.calls++
	f.lastUserID = in.UserID
	return memory.Fact{}, nil
}

func (f *fakeKnowledge) DeprecateKnowledge(_ context.Context, in memorywrite.KnowledgeDeprecateInput) (memory.Fact, error) {
	f.calls++
	f.lastUserID = in.UserID
	f.lastDeprecatedBy = in.DeprecatedBy
	return memory.Fact{}, nil
}

func (f *fakeKnowledge) RestoreKnowledge(_ context.Context, in memorywrite.KnowledgeRestoreInput) (memorywrite.KnowledgeRestoreResult, error) {
	f.calls++
	f.lastUserID = in.UserID
	f.lastRestoredBy = in.RestoredBy
	return memorywrite.KnowledgeRestoreResult{}, nil
}

func (f *fakeKnowledge) ReadChangelogPage(_ context.Context, userID string, _ string, _ string, _ *memory.ChangelogCursor, _ int) ([]memory.ChangeEntry, error) {
	f.calls++
	f.lastChangelogOwner = userID
	return nil, nil
}

// TestAgentGateFailsClosedBeforeAnyStoreAccess proves every gated use case
// authorizes agent access first: a denied authority returns the agentaccess
// sentinel and never reads or writes the profile store (and never reaches the
// nil query handle, which would panic).
func TestAgentGateFailsClosedBeforeAnyStoreAccess(t *testing.T) {
	ctx := context.Background()
	profiles := &fakeProfiles{}
	knowledge := &fakeKnowledge{}
	deny := &fakeAuthorizer{err: agentaccess.ErrForbidden}
	// db is nil: any query before the gate would panic, proving order.
	svc := NewService(nil, profiles, nil, knowledge, deny, func() string { return "D" }, nil)
	auth := userAuthority(t, false)

	assertDenied := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, agentaccess.ErrForbidden) {
			t.Fatalf("%s = %v, want ErrForbidden", name, err)
		}
	}

	_, err := svc.Memory(ctx, auth, "a")
	assertDenied("Memory", err)
	_, err = svc.SetSoul(ctx, auth, "a", "x")
	assertDenied("SetSoul", err)
	_, err = svc.ListConstraints(ctx, auth, "a")
	assertDenied("ListConstraints", err)
	_, err = svc.AddConstraint(ctx, auth, "a", "x")
	assertDenied("AddConstraint", err)
	_, err = svc.RemoveConstraint(ctx, auth, "a", "c")
	assertDenied("RemoveConstraint", err)
	_, err = svc.Changelog(ctx, auth, "a", []string{"profile"}, 10)
	assertDenied("Changelog", err)
	_, err = svc.ListKnowledge(ctx, auth, "a", KnowledgeStateActive, 10, nil)
	assertDenied("ListKnowledge", err)
	_, err = svc.CreateKnowledge(ctx, auth, "a", "x")
	assertDenied("CreateKnowledge", err)
	_, err = svc.ReplaceKnowledge(ctx, auth, "a", "f", "x")
	assertDenied("ReplaceKnowledge", err)
	assertDenied("DeprecateKnowledge", svc.DeprecateKnowledge(ctx, auth, "a", "f"))
	_, err = svc.RestoreKnowledge(ctx, auth, "a", "f")
	assertDenied("RestoreKnowledge", err)
	_, err = svc.ChangelogPage(ctx, auth, "a", "knowledge", nil, 10)
	assertDenied("ChangelogPage", err)
	_, err = svc.SetUserMemory(ctx, auth, "u1", "a", "x")
	assertDenied("SetUserMemory", err)
	assertDenied("DeleteUserMemory", svc.DeleteUserMemory(ctx, auth, "u1", "a"))
	_, err = svc.ListUserMemories(ctx, authz.Authority{}, "u1")
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("ListUserMemories invalid authority = %v, want authz.ErrForbidden", err)
	}
	_, err = svc.ListUserMemories(ctx, auth, "u2")
	if !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("ListUserMemories foreign target = %v, want authz.ErrNotFound", err)
	}

	if profiles.reads != 0 || profiles.writes != 0 {
		t.Fatalf("store touched before gate: reads=%d writes=%d", profiles.reads, profiles.writes)
	}
	if knowledge.calls != 0 {
		t.Fatalf("knowledge manager touched before gate: calls=%d", knowledge.calls)
	}
	if deny.calls == 0 {
		t.Fatal("authorizer never consulted")
	}
}

func TestUserMemoryWritesAuthorizeTargetBeforeAgentOrStore(t *testing.T) {
	ctx := context.Background()
	profiles := &fakeProfiles{}
	authorizer := &fakeAuthorizer{err: agentaccess.ErrForbidden}
	svc := NewService(nil, profiles, nil, nil, authorizer, nil, nil)
	user := userAuthority(t, false)

	if _, err := svc.SetUserMemory(ctx, user, "u2", "a", "x"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("SetUserMemory foreign target = %v, want authz.ErrNotFound", err)
	}
	if err := svc.DeleteUserMemory(ctx, user, "u2", "a"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("DeleteUserMemory foreign target = %v, want authz.ErrNotFound", err)
	}
	if authorizer.calls != 0 || profiles.reads != 0 || profiles.writes != 0 {
		t.Fatalf("foreign target touched downstream boundary: authorizer=%d reads=%d writes=%d", authorizer.calls, profiles.reads, profiles.writes)
	}

	admin := userAuthority(t, true)
	if _, err := svc.SetUserMemory(ctx, admin, "u2", "a", "x"); !errors.Is(err, agentaccess.ErrForbidden) {
		t.Fatalf("SetUserMemory admin target = %v, want downstream ErrForbidden", err)
	}
	if err := svc.DeleteUserMemory(ctx, admin, "u2", "a"); !errors.Is(err, agentaccess.ErrForbidden) {
		t.Fatalf("DeleteUserMemory admin target = %v, want downstream ErrForbidden", err)
	}
	if authorizer.calls != 2 {
		t.Fatalf("admin target authorizer calls = %d, want 2", authorizer.calls)
	}
}

// TestKnowledgeOwnerComesFromAuthority proves the knowledge boundary derives the
// owner tuple and the deprecating/restoring actor from the trusted Authority, not
// from any caller-supplied field: the forwarded ids are always the authority's
// user id.
func TestKnowledgeOwnerComesFromAuthority(t *testing.T) {
	ctx := context.Background()
	allow := &fakeAuthorizer{}
	knowledge := &fakeKnowledge{}
	svc := NewService(nil, &fakeProfiles{}, nil, knowledge, allow, nil, nil)
	auth := userAuthority(t, false) // user id "u1"

	if _, err := svc.CreateKnowledge(ctx, auth, "a", "x"); err != nil {
		t.Fatalf("CreateKnowledge: %v", err)
	}
	if knowledge.lastUserID != "u1" {
		t.Fatalf("CreateKnowledge owner = %q, want authority user %q", knowledge.lastUserID, "u1")
	}
	if err := svc.DeprecateKnowledge(ctx, auth, "a", "f"); err != nil {
		t.Fatalf("DeprecateKnowledge: %v", err)
	}
	if knowledge.lastUserID != "u1" || knowledge.lastDeprecatedBy != "u1" {
		t.Fatalf("Deprecate owner/actor = %q/%q, want u1/u1", knowledge.lastUserID, knowledge.lastDeprecatedBy)
	}
	if _, err := svc.RestoreKnowledge(ctx, auth, "a", "f"); err != nil {
		t.Fatalf("RestoreKnowledge: %v", err)
	}
	if knowledge.lastUserID != "u1" || knowledge.lastRestoredBy != "u1" {
		t.Fatalf("Restore owner/actor = %q/%q, want u1/u1", knowledge.lastUserID, knowledge.lastRestoredBy)
	}
	if _, err := svc.ChangelogPage(ctx, auth, "a", "knowledge", nil, 10); err != nil {
		t.Fatalf("ChangelogPage: %v", err)
	}
	if knowledge.lastChangelogOwner != "u1" {
		t.Fatalf("ChangelogPage owner = %q, want authority user %q", knowledge.lastChangelogOwner, "u1")
	}
}

// TestUnavailableStoresFailClosed proves a Provider missing a capability degrades
// to a typed 503-mapped error after the gate passes, never a nil-deref.
func TestUnavailableStoresFailClosed(t *testing.T) {
	ctx := context.Background()
	allow := &fakeAuthorizer{}
	auth := userAuthority(t, false)

	// No ProfileStore: a soul write reports the profile-store-unavailable error.
	svc := NewService(nil, nil, nil, nil, allow, nil, nil)
	if _, err := svc.SetSoul(ctx, auth, "a", "x"); !errors.Is(err, ErrProfileStoreUnavailable) {
		t.Fatalf("SetSoul without ProfileStore = %v, want ErrProfileStoreUnavailable", err)
	}
	if _, err := svc.SetUserMemory(ctx, auth, "u1", "a", "x"); !errors.Is(err, ErrProfileStoreUnavailable) {
		t.Fatalf("SetUserMemory without ProfileStore = %v, want ErrProfileStoreUnavailable", err)
	}
	// No ChangelogReader: a profile-scope changelog read reports its own error.
	if _, err := svc.Changelog(ctx, auth, "a", []string{"profile"}, 10); !errors.Is(err, ErrChangelogReaderUnavailable) {
		t.Fatalf("Changelog without reader = %v, want ErrChangelogReaderUnavailable", err)
	}
}

func TestChangeSourceAudit(t *testing.T) {
	admin := userAuthority(t, true)
	user := userAuthority(t, false)

	// Admin acting on another user's memory is a System change.
	if got := changeSource(admin, "other"); got != memory.SourceSystem {
		t.Fatalf("admin->other = %q, want system", got)
	}
	// Admin acting on their own memory is a User change.
	if got := changeSource(admin, "u1"); got != memory.SourceUser {
		t.Fatalf("admin->self = %q, want user", got)
	}
	// A non-admin is always a User change.
	if got := changeSource(user, "u1"); got != memory.SourceUser {
		t.Fatalf("user->self = %q, want user", got)
	}
}

func TestMemoryFromRowDefaultSoulAndProjection(t *testing.T) {
	ctx := context.Background()
	svc := &Service{profiles: &fakeProfiles{content: "PROFILE", soul: ""}, defaultSoul: func() string { return "DEFAULT_SOUL" }}
	row := sqlc.CtxAgentMemory{
		UserID: "u1", AgentID: "a", Version: 3,
		Constraints:    []byte(`[{"id":"c1","text":"no rm","created_at":"2026-01-01T00:00:00Z"}]`),
		ProfileEntries: []byte(`[]`),
	}
	m, err := svc.memoryFromRow(ctx, row)
	if err != nil {
		t.Fatalf("memoryFromRow: %v", err)
	}
	if m.Content != "PROFILE" {
		t.Fatalf("content = %q, want fact-backed PROFILE", m.Content)
	}
	if m.Soul != "DEFAULT_SOUL" || m.SoulSource != SoulSourceBuiltin {
		t.Fatalf("soul = %q (%s), want built-in default when store and agent are empty", m.Soul, m.SoulSource)
	}
	if m.Version != 3 || len(m.Constraints) != 1 || m.Constraints[0].ID != "c1" {
		t.Fatalf("projection = %+v", m)
	}
	// Empty profile entries decode to a non-nil slice so the JSON stays [].
	if m.ProfileEntries == nil {
		t.Fatal("profile entries should be non-nil empty slice, not nil")
	}

	// A stored soul is preserved verbatim.
	svc.profiles = &fakeProfiles{content: "P", soul: "STORED"}
	m, err = svc.memoryFromRow(ctx, row)
	if err != nil {
		t.Fatalf("memoryFromRow(stored soul): %v", err)
	}
	if m.Soul != "STORED" || m.SoulSource != SoulSourceUser {
		t.Fatalf("soul = %q (%s), want stored soul", m.Soul, m.SoulSource)
	}

	// The agent's configured default outranks the built-in one, matching the
	// prompt builder's resolution order, and a stored soul still wins over both.
	svc.agentSoul = func(_ context.Context, agentID string) (string, error) {
		if agentID != "a" {
			t.Fatalf("agentSoul asked for %q, want a", agentID)
		}
		return "AGENT_SOUL", nil
	}
	svc.profiles = &fakeProfiles{content: "P", soul: ""}
	m, err = svc.memoryFromRow(ctx, row)
	if err != nil {
		t.Fatalf("memoryFromRow(agent soul): %v", err)
	}
	if m.Soul != "AGENT_SOUL" || m.SoulSource != SoulSourceAgent {
		t.Fatalf("soul = %q (%s), want agent default", m.Soul, m.SoulSource)
	}
	svc.profiles = &fakeProfiles{content: "P", soul: "STORED"}
	if m, err = svc.memoryFromRow(ctx, row); err != nil || m.SoulSource != SoulSourceUser {
		t.Fatalf("soul source = %q (err %v), want user override over agent default", m.SoulSource, err)
	}
}

func TestChangeEntryFromRowConversion(t *testing.T) {
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	row := sqlc.CtxAgentMemoryChangelog{
		ID: "e1", UserID: "u1", AgentID: "a",
		Scope:               "constraint",
		Action:              "delete",
		Source:              "manual",
		MemoryVersionBefore: pgtype.Int8{Int64: 4, Valid: true},
		MemoryVersionAfter:  pgtype.Int8{Valid: false},
		BeforeText:          pgtype.Text{String: "old rule", Valid: true},
		AfterText:           pgtype.Text{Valid: false},
		CreatedAt:           created,
	}
	got := changeEntryFromRow(row)
	if got.ID != "e1" || got.Scope != "constraint" || got.Action != "delete" {
		t.Fatalf("basic fields = %+v", got)
	}
	if got.Source != memory.SourceManual {
		t.Fatalf("source = %q, want manual", got.Source)
	}
	if got.MemoryVersionBefore == nil || *got.MemoryVersionBefore != 4 {
		t.Fatalf("version before = %v, want 4", got.MemoryVersionBefore)
	}
	if got.MemoryVersionAfter != nil {
		t.Fatalf("version after = %v, want nil (invalid)", got.MemoryVersionAfter)
	}
	if got.BeforeText != "old rule" || got.AfterText != "" {
		t.Fatalf("text = (%q,%q), want (old rule, \"\")", got.BeforeText, got.AfterText)
	}
	// CreatedAt round-trips through RFC3339Nano, preserving the instant.
	if got.CreatedAt != created.Format(time.RFC3339Nano) {
		t.Fatalf("created = %q, want %q", got.CreatedAt, created.Format(time.RFC3339Nano))
	}
}
