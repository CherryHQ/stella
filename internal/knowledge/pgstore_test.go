package knowledge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	cfgstore "github.com/CherryHQ/stella/internal/store"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func newTestStore(t *testing.T) (*PGStore, *pgxpool.Pool, context.Context) {
	t.Helper()
	db := dbtest.New(t)
	return New(db), db, context.Background()
}

func seedFixtures(t *testing.T, db *pgxpool.Pool) (string, string) {
	t.Helper()
	ctx := context.Background()

	oidcStore := appdb.NewOIDCStore(db)
	u, err := oidcStore.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "knowledge@test.local",
		Name:  "knowledge",
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	agentID := "agent-knowledge"
	cs := cfgstore.NewDBStore(db)
	if err := cs.CreateAgent(ctx, config.Agent{
		ID: agentID, Name: agentID, Model: "p/m", Workspace: "/tmp/" + agentID, Enabled: true,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return u.ID, agentID
}

func TestListActiveKnowledgeVisibilityAndStatus(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	expiredAt := time.Now().Add(-time.Hour)

	create := func(name string, params CreateParams) {
		t.Helper()
		params.Name = name
		params.Content = name + " content"
		if params.Kind == "" {
			params.Kind = KindFact
		}
		if params.Status == "" {
			params.Status = StatusActive
		}
		if _, err := store.Create(ctx, params); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	create("system", CreateParams{Scope: "system"})
	create("system-agent", CreateParams{Scope: "system_agent", AgentID: agentID})
	create("user", CreateParams{Scope: "user", UserID: userID})
	create("user-agent", CreateParams{Scope: "user_agent", UserID: userID, AgentID: agentID})
	create("draft", CreateParams{Scope: "user", UserID: userID, Status: StatusDraft})
	create("deprecated", CreateParams{Scope: "user", UserID: userID, Status: StatusDeprecated})
	create("expired", CreateParams{Scope: "user", UserID: userID, ExpiresAt: &expiredAt})

	rows, err := store.ListActive(ctx, ViewContext{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	got := map[string]bool{}
	for _, row := range rows {
		got[row.Name] = true
	}
	for _, name := range []string{"system", "system-agent", "user", "user-agent"} {
		if !got[name] {
			t.Fatalf("expected visible active knowledge %q in %#v", name, got)
		}
	}
	for _, name := range []string{"draft", "deprecated", "expired"} {
		if got[name] {
			t.Fatalf("did not expect %q in active knowledge %#v", name, got)
		}
	}
}

func TestExpireDraftsByType(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)

	fact, err := store.Create(ctx, CreateParams{
		Kind: KindFact, Scope: "user", UserID: userID, Name: "old-fact", Content: "old fact", Status: StatusDraft,
	})
	if err != nil {
		t.Fatalf("create fact: %v", err)
	}
	contextEntry, err := store.Create(ctx, CreateParams{
		Kind: KindContext, Scope: "user", UserID: userID, Name: "old-context", Content: "old context", Status: StatusDraft,
	})
	if err != nil {
		t.Fatalf("create context: %v", err)
	}

	old := time.Now().Add(-48 * time.Hour)
	if _, err := db.Exec(ctx, `UPDATE agent_knowledge SET created_at=$1 WHERE id IN ($2, $3)`, old, fact.ID, contextEntry.ID); err != nil {
		t.Fatalf("age drafts: %v", err)
	}
	if err := store.ExpireDraftsByType(ctx, KindFact, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatalf("ExpireDraftsByType: %v", err)
	}

	gotFact, err := store.Get(ctx, fact.ID)
	if err != nil {
		t.Fatalf("get fact: %v", err)
	}
	gotContext, err := store.Get(ctx, contextEntry.ID)
	if err != nil {
		t.Fatalf("get context: %v", err)
	}
	if gotFact.Status != StatusDeprecated {
		t.Fatalf("expected fact deprecated, got %s", gotFact.Status)
	}
	if gotContext.Status != StatusDraft {
		t.Fatalf("expected context to remain draft, got %s", gotContext.Status)
	}
}

func TestDeprecatedKnowledgeDoesNotBlockSameNameCreate(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)

	oldEntry, err := store.Create(ctx, CreateParams{
		Kind: KindFact, Scope: "user", UserID: userID, Name: "replaceable", Content: "old", Status: StatusActive,
	})
	if err != nil {
		t.Fatalf("create old entry: %v", err)
	}
	if err := store.Deprecate(ctx, oldEntry.ID); err != nil {
		t.Fatalf("deprecate old entry: %v", err)
	}

	if _, err := store.Create(ctx, CreateParams{
		Kind: KindFact, Scope: "user", UserID: userID, Name: "replaceable", Content: "new", Status: StatusDraft,
	}); err != nil {
		t.Fatalf("create replacement after deprecate: %v", err)
	}
}

func TestListByNameAndScopeExcludesDeprecatedKnowledge(t *testing.T) {
	store, db, ctx := newTestStore(t)
	userID, _ := seedFixtures(t, db)

	oldEntry, err := store.Create(ctx, CreateParams{
		Kind: KindFact, Scope: "user", UserID: userID, Name: "replaceable", Content: "old", Status: StatusActive,
	})
	if err != nil {
		t.Fatalf("create old entry: %v", err)
	}
	if err := store.Deprecate(ctx, oldEntry.ID); err != nil {
		t.Fatalf("deprecate old entry: %v", err)
	}
	replacement, err := store.Create(ctx, CreateParams{
		Kind: KindFact, Scope: "user", UserID: userID, Name: "replaceable", Content: "new", Status: StatusDraft,
	})
	if err != nil {
		t.Fatalf("create replacement after deprecate: %v", err)
	}

	// Tool patch/deprecate resolution should only see the live replacement row.
	rows, err := store.ListByNameAndScope(ctx, "replaceable", "user", userID, "")
	if err != nil {
		t.Fatalf("list replacement by name and scope: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one non-deprecated row, got %d: %+v", len(rows), rows)
	}
	if rows[0].ID != replacement.ID || rows[0].Status == StatusDeprecated {
		t.Fatalf("unexpected replacement row: %+v", rows[0])
	}
}

func TestListActiveRejectsMultipleKindFilters(t *testing.T) {
	store, _, ctx := newTestStore(t)

	_, err := store.ListActive(ctx, ViewContext{}, KindFact, KindContext)
	if err == nil {
		t.Fatal("expected multiple kind filters to fail")
	}
	if !strings.Contains(err.Error(), "multiple kind filters") {
		t.Fatalf("error = %v, want multiple kind filters", err)
	}
}
