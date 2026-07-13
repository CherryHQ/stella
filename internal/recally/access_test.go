package recally

import (
	"context"
	"errors"
	"testing"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
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

func userAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	rs, err := authz.NewRoleSet(authz.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	a, err := authz.NewUserAuthority(authz.UserID(id), rs, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func agentAuthority(t *testing.T, userID, agentID string) authz.Authority {
	t.Helper()
	a, err := agentaccess.WorkerAgentAuthority(userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// mustBegin opens one recally Access as the given user; used across the recally
// service tests now that the identity facade is gone.
func mustBegin(t *testing.T, svc *Service, userID string) *Access {
	t.Helper()
	acc, err := svc.Begin(t.Context(), userAuthority(t, userID))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return acc
}

func TestRecallyBeginRejectsInvalidAuthority(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), t.TempDir(), policy.New(db))
	if _, err := svc.Begin(context.Background(), authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Begin(zero) err=%v, want forbidden", err)
	}
}

// The owner can save and read back their own article in one round-trip.
func TestRecallyOwnerSaveGetRoundTrips(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()
	svc := NewService(NewStore(db), t.TempDir(), policy.New(db))

	saved, err := mustBegin(t, svc, testUserID).Save(ctx, SaveRequest{URL: "https://example.com/own", Title: "Own", Content: "body"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := mustBegin(t, svc, testUserID).GetArticle(ctx, saved.Article.ID)
	if err != nil {
		t.Fatalf("GetArticle: %v", err)
	}
	if got.ID != saved.Article.ID {
		t.Fatalf("GetArticle id=%s, want %s", got.ID, saved.Article.ID)
	}
}

// A foreign user cannot read another user's article: the uid-scoped store hides
// it as an opaque not-found, never leaking existence.
func TestRecallyForeignUserCannotRead(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, 'foreign@example.com')`, otherUserID); err != nil {
		t.Fatalf("insert foreign user: %v", err)
	}
	svc := NewService(NewStore(db), t.TempDir(), policy.New(db))

	saved, err := mustBegin(t, svc, testUserID).Save(ctx, SaveRequest{URL: "https://example.com/secret", Title: "Secret", Content: "body"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	foreign := mustBegin(t, svc, otherUserID)
	if _, err := foreign.GetArticle(ctx, saved.Article.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign GetArticle err=%v, want ErrNotFound", err)
	}
	if err := foreign.UpsertArticleContent(ctx, saved.Article.ID, "stolen"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign UpsertArticleContent err=%v, want ErrNotFound", err)
	}
	body, err := mustBegin(t, svc, testUserID).ReadArticleBody(ctx, saved.Article)
	if err != nil {
		t.Fatalf("owner ReadArticleBody: %v", err)
	}
	if body != "body" {
		t.Fatalf("body=%q, want original content", body)
	}
}

// A delegated agent has the SAME access as its delegating user (recally is
// user-owned, shared across the user's agents); exactly one Begin per use case.
func TestRecallyAgentActsAsUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()
	az := &countingAuthorizer{Authorizer: policy.New(db)}
	svc := NewService(NewStore(db), t.TempDir(), az)

	saved, err := mustBegin(t, svc, testUserID).Save(ctx, SaveRequest{URL: "https://example.com/shared", Title: "Shared", Content: "body"})
	if err != nil {
		t.Fatalf("owner Save: %v", err)
	}

	acc, err := svc.Begin(ctx, agentAuthority(t, testUserID, "agent-x"))
	if err != nil {
		t.Fatalf("agent Begin: %v", err)
	}
	before := az.begins
	got, err := acc.GetArticle(ctx, saved.Article.ID)
	if err != nil {
		t.Fatalf("agent GetArticle: %v", err)
	}
	if got.ID != saved.Article.ID {
		t.Fatalf("agent GetArticle id=%s, want %s", got.ID, saved.Article.ID)
	}
	if az.begins != before {
		t.Fatalf("GetArticle opened %d extra Begins, want 0 within one Access", az.begins-before)
	}
}

// A custom deny on is_owner read overrides the owner built-in and hides the row
// as an opaque not-found. This asserts the same shape as the email/scheduler PEP
// tests; it requires ResourceRecally to accept custom policies (activation).
func TestRecallyCustomDenyHidesOwnArticle(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()
	svc := NewService(NewStore(db), t.TempDir(), policy.New(db))

	saved, err := mustBegin(t, svc, testUserID).Save(ctx, SaveRequest{URL: "https://example.com/deny", Title: "Deny", Content: "body"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	ps := policy.NewService(policy.New(db))
	if _, _, err := ps.CreatePolicy(ctx, policy.PolicyInput{
		Name: "deny own recally read", Resource: authz.ResourceRecally, Action: authz.ActionRead,
		Effect: policy.EffectDeny, Subjects: policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
		Predicates: []policy.Predicate{policy.Eq("is_owner", "true")},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mustBegin(t, svc, testUserID).GetArticle(ctx, saved.Article.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("custom deny GetArticle err=%v, want ErrNotFound", err)
	}
}
