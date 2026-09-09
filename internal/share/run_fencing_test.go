package share_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/library/recally"
)

func TestLostRunCannotCreateOrRevokeShare(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	userID := seedShareUser(t, db, "run-fence")
	store := recally.NewStore(db)
	articleID := saveArticle(t, db, store, t.TempDir(), userID)
	access := mustAccess(t, newShareService(t, db), userAuthority(t, userID))
	lost := agentrun.WithGuard(ctx, agentrun.Guard{
		RunID: uuid.NewString(), SessionID: "lost-session", ExecutorBootID: uuid.NewString(),
	})
	for name, guard := range map[string]agentrun.Guard{
		"missing run":   {RunID: uuid.NewString(), SessionID: "lost-session", ExecutorBootID: uuid.NewString()},
		"empty guard":   {},
		"partial guard": {RunID: uuid.NewString()},
	} {
		t.Run(name, func(t *testing.T) {
			want := agentrun.ErrLeaseLost
			if name != "missing run" {
				want = agentrun.ErrInvalidGuard
			}
			if _, err := access.ShareArticle(agentrun.WithGuard(ctx, guard), articleID, ""); !errors.Is(err, want) {
				t.Fatalf("create with invalid Run = %v, want %v", err, want)
			}
		})
	}
	shares, err := access.List(ctx, 10, 0)
	if err != nil || len(shares.Shares) != 0 {
		t.Fatalf("lost Run committed share: count=%d err=%v", len(shares.Shares), err)
	}
	created, err := access.ShareArticle(ctx, articleID, "")
	if err != nil {
		t.Fatalf("authorized user create: %v", err)
	}
	if err := access.Revoke(lost, created.Share.ID); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("revoke with lost Run = %v, want ErrLeaseLost", err)
	}
	if _, err := newShareService(t, db).PublicContent(ctx, created.Token); err != nil {
		t.Fatalf("lost Run removed public share: %v", err)
	}
	if err := access.Revoke(ctx, created.Share.ID); err != nil {
		t.Fatalf("authorized user revoke: %v", err)
	}
}
