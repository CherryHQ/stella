package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// agentAllow is a complete, valid Agent policy input for lifecycle tests.
func agentAllow() PolicyInput {
	return PolicyInput{
		Name:       "agent read allow",
		Resource:   authz.ResourceAgent,
		Action:     authz.ActionRead,
		Effect:     EffectAllow,
		Subjects:   NewSubjectBuilder().Roles(authz.RoleUser).Build(),
		Predicates: []Predicate{Eq("scope", "system")},
	}
}

// seedRow inserts a policy row directly (bypassing the mutation service) with a
// given status, for lifecycle preconditions.
func seedRow(t *testing.T, pool *pgxpool.Pool, id, status string) {
	t.Helper()
	_, err := sqlc.New(pool).CreateAuthzPolicy(context.Background(), sqlc.CreateAuthzPolicyParams{
		ID:               id,
		Name:             id,
		ResourceType:     "",
		Action:           "",
		Effect:           string(EffectAllow),
		Subjects:         []byte(`{}`),
		Attributes:       []byte(`{}`),
		CatalogVersion:   0,
		Status:           status,
		QuarantineReason: "seeded",
	})
	if err != nil {
		t.Fatalf("seed %s row: %v", status, err)
	}
}

func statusOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	row, err := sqlc.New(pool).GetAuthzPolicy(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return row.Status
}

func TestUpdateAndDeleteNotFoundMapping(t *testing.T) {
	ctx := context.Background()
	svc := NewService(New(dbtest.New(t)))

	if _, err := svc.UpdatePolicy(ctx, "missing", agentAllow()); !errors.Is(err, ErrPolicyNotFound) {
		t.Fatalf("update missing = %v, want ErrPolicyNotFound", err)
	}
	if _, err := svc.DeletePolicy(ctx, "missing"); !errors.Is(err, ErrPolicyNotFound) {
		t.Fatalf("delete missing = %v, want ErrPolicyNotFound", err)
	}
}

// A connection failure must NOT be misreported as ErrPolicyNotFound.
func TestGetErrorPropagatesNotAsNotFound(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	svc := NewService(New(pool))
	pool.Close()

	_, err := svc.DeletePolicy(ctx, "anything")
	if err == nil {
		t.Fatal("delete on a closed pool must error")
	}
	if errors.Is(err, ErrPolicyNotFound) {
		t.Fatalf("connection failure was misreported as ErrPolicyNotFound: %v", err)
	}
}

// UpdatePolicy must never silently activate a quarantined/inactive row.
func TestUpdateRejectsNonActiveRow(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	svc := NewService(New(pool))

	seedRow(t, pool, "q1", statusQuarantined)
	if _, err := svc.UpdatePolicy(ctx, "q1", agentAllow()); !errors.Is(err, ErrPolicyNotActive) {
		t.Fatalf("update quarantined = %v, want ErrPolicyNotActive", err)
	}
	if got := statusOf(t, pool, "q1"); got != statusQuarantined {
		t.Fatalf("rejected update changed status to %q, want quarantined", got)
	}
	if got := currentRevision(t, pool); got != 0 {
		t.Fatalf("rejected update bumped revision to %d, want 0", got)
	}
}

func TestUpdateActiveRowBumpsRevision(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	svc := NewService(New(pool))

	id, rev, err := svc.CreatePolicy(ctx, agentAllow())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rev != 1 {
		t.Fatalf("create rev = %d, want 1", rev)
	}
	rev2, err := svc.UpdatePolicy(ctx, id, agentAllow())
	if err != nil {
		t.Fatalf("update active: %v", err)
	}
	if rev2 != 2 {
		t.Fatalf("update rev = %d, want 2", rev2)
	}
}

// ActivatePolicy requires a quarantined/inactive row plus a complete input, and
// atomically flips it to active while bumping the revision.
func TestActivatePolicyFromQuarantined(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	svc := NewService(New(pool))

	seedRow(t, pool, "q1", statusQuarantined)
	rev, err := svc.ActivatePolicy(ctx, "q1", agentAllow())
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if rev != 1 {
		t.Fatalf("activate rev = %d, want 1", rev)
	}
	if got := statusOf(t, pool, "q1"); got != statusActive {
		t.Fatalf("status after activate = %q, want active", got)
	}
}

func TestActivateRejectsAlreadyActive(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	svc := NewService(New(pool))

	id, _, err := svc.CreatePolicy(ctx, agentAllow())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.ActivatePolicy(ctx, id, agentAllow()); !errors.Is(err, ErrPolicyAlreadyActive) {
		t.Fatalf("activate active = %v, want ErrPolicyAlreadyActive", err)
	}
}

// Activation can only target an active vertical; an inactive resource is
// rejected before any row is touched.
func TestActivateRejectsInactiveResource(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	svc := NewService(New(pool))

	seedRow(t, pool, "q1", statusQuarantined)
	in := agentAllow()
	in.Resource = authz.ResourceUser // not cut over yet (user management)
	in.Predicates = nil
	if _, err := svc.ActivatePolicy(ctx, "q1", in); !errors.Is(err, ErrResourceInactive) {
		t.Fatalf("activate inactive resource = %v, want ErrResourceInactive", err)
	}
	if got := statusOf(t, pool, "q1"); got != statusQuarantined {
		t.Fatalf("rejected activate changed status to %q, want quarantined", got)
	}
	if got := currentRevision(t, pool); got != 0 {
		t.Fatalf("rejected activate bumped revision to %d, want 0", got)
	}
}
