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

// TestAgentGateFailsClosedBeforeAnyStoreAccess proves every gated use case
// authorizes agent access first: a denied authority returns the agentaccess
// sentinel and never reads or writes the profile store (and never reaches the
// nil query handle, which would panic).
func TestAgentGateFailsClosedBeforeAnyStoreAccess(t *testing.T) {
	ctx := context.Background()
	profiles := &fakeProfiles{}
	deny := &fakeAuthorizer{err: agentaccess.ErrForbidden}
	// db is nil: any query before the gate would panic, proving order.
	svc := NewService(nil, profiles, nil, deny, func() string { return "D" }, nil)
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
	_, err = svc.SetUserMemory(ctx, auth, "u2", "a", "x")
	assertDenied("SetUserMemory", err)
	assertDenied("DeleteUserMemory", svc.DeleteUserMemory(ctx, auth, "u2", "a"))
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
	if deny.calls == 0 {
		t.Fatal("authorizer never consulted")
	}
}

// TestUnavailableStoresFailClosed proves a Provider missing a capability degrades
// to a typed 503-mapped error after the gate passes, never a nil-deref.
func TestUnavailableStoresFailClosed(t *testing.T) {
	ctx := context.Background()
	allow := &fakeAuthorizer{}
	auth := userAuthority(t, false)

	// No ProfileStore: a soul write reports the profile-store-unavailable error.
	svc := NewService(nil, nil, nil, allow, nil, nil)
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
	if m.Soul != "DEFAULT_SOUL" {
		t.Fatalf("soul = %q, want default when store returns empty", m.Soul)
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
	if m.Soul != "STORED" {
		t.Fatalf("soul = %q, want stored soul", m.Soul)
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
