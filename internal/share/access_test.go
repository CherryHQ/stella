package share_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/recally"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// countingAuthorizer proves the PEP opens exactly one Begin per use case.
type countingAuthorizer struct {
	authz.Authorizer
	begins int
}

func (a *countingAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	a.begins++
	return a.Authorizer.Begin(ctx, authority)
}

// saveArticle stores a recally article for userID and returns its id.
func saveArticle(t *testing.T, db *pgxpool.Pool, store *recally.Store, home, userID string) string {
	t.Helper()
	acc, err := recally.NewService(store, home, policy.New(db)).Begin(context.Background(), userAuthority(t, userID))
	if err != nil {
		t.Fatalf("recally Begin: %v", err)
	}
	saved, err := acc.Save(context.Background(), recally.SaveRequest{URL: "https://example.com/a", Title: "A", Content: "body"})
	if err != nil {
		t.Fatalf("Save article: %v", err)
	}
	return saved.Article.ID
}

func TestShareBeginRejectsInvalidAuthority(t *testing.T) {
	db := dbtest.New(t)
	svc := newShareService(t, db)
	if _, err := svc.Begin(context.Background(), authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Begin(zero) err=%v, want forbidden", err)
	}
}

// An owner shares an article, lists it, then revokes it — all within one Access
// per use case (the counting authorizer proves no hidden re-Begin).
func TestShareOwnerRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store := recally.NewStore(db)
	home := t.TempDir()
	az := &countingAuthorizer{Authorizer: policy.New(db)}
	svc := sharepkg.NewService(sqlc.New(db), memorytest.New(), store, mustAssets(t, home, nil), home, "http://stella.test", az)
	userID := seedShareUser(t, db, "owner")
	articleID := saveArticle(t, db, store, home, userID)

	acc := mustBegin(t, svc, userAuthority(t, userID))
	before := az.begins
	created, err := acc.ShareArticle(ctx, articleID, "7d")
	if err != nil {
		t.Fatalf("ShareArticle: %v", err)
	}
	list, err := acc.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Shares) != 1 || list.Shares[0].ID != created.Share.ID {
		t.Fatalf("List=%+v, want the one created share", list.Shares)
	}
	if err := acc.Revoke(ctx, created.Share.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if again, err := acc.List(ctx, 10, 0); err != nil || len(again.Shares) != 0 {
		t.Fatalf("post-revoke List=%+v err=%v, want empty", again, err)
	}
	if az.begins != before {
		t.Fatalf("owner round-trip opened %d extra Begins, want 0 within one Access", az.begins-before)
	}
}

// A foreign user sees none of the owner's shares and cannot revoke one.
func TestShareForeignUserIsolated(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store := recally.NewStore(db)
	home := t.TempDir()
	svc := sharepkg.NewService(sqlc.New(db), memorytest.New(), store, mustAssets(t, home, nil), home, "http://stella.test", policy.New(db))
	ownerID := seedShareUser(t, db, "owner")
	foreignID := seedShareUser(t, db, "foreign")
	articleID := saveArticle(t, db, store, home, ownerID)

	created, err := mustBegin(t, svc, userAuthority(t, ownerID)).ShareArticle(ctx, articleID, "7d")
	if err != nil {
		t.Fatalf("owner ShareArticle: %v", err)
	}

	foreign := mustBegin(t, svc, userAuthority(t, foreignID))
	if list, err := foreign.List(ctx, 10, 0); err != nil || len(list.Shares) != 0 {
		t.Fatalf("foreign List=%+v err=%v, want empty", list, err)
	}
	if err := foreign.Revoke(ctx, created.Share.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign Revoke err=%v, want ErrNotFound", err)
	}
}

// An agent-scoped actor cannot select a different agent's workspace; the Access
// confines it to its bound agent before any filesystem access.
func TestShareAgentCannotShareForeignAgentWorkspace(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	svc := newShareService(t, db)
	userID := seedShareUser(t, db, "agent")

	acc := mustBegin(t, svc, agentAuthority(t, userID, "agent-self"))
	if _, err := acc.ShareArtifact(ctx, "session", "ok.html", "", "agent-other", "7d"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("cross-agent ShareArtifact err=%v, want ErrForbidden", err)
	}
}

// A custom deny on is_owner create overrides the owner built-in. Requires the
// integrator's ResourceShare activation flip; until then CreatePolicy is rejected.
func TestShareCustomDenyBlocksCreate(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store := recally.NewStore(db)
	home := t.TempDir()
	userID := seedShareUser(t, db, "deny")
	articleID := saveArticle(t, db, store, home, userID)

	ps := policy.NewService(policy.New(db))
	if _, _, err := ps.CreatePolicy(ctx, policy.PolicyInput{
		Name: "deny own share create", Resource: authz.ResourceShare, Action: authz.ActionCreate,
		Effect: policy.EffectDeny, Subjects: policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
		Predicates: []policy.Predicate{policy.Eq("is_owner", "true")},
	}); err != nil {
		t.Fatal(err)
	}

	svc := sharepkg.NewService(sqlc.New(db), memorytest.New(), store, mustAssets(t, home, nil), home, "http://stella.test", policy.New(db))
	acc := mustBegin(t, svc, userAuthority(t, userID))
	if _, err := acc.ShareArticle(ctx, articleID, "7d"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("custom-deny ShareArticle err=%v, want ErrForbidden", err)
	}
}
