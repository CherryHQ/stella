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

// seedAgentRow inserts a single agent-read-allow row directly with a chosen
// status and catalog_version. Subjects/attributes are valid so the ONLY variable
// under test is the catalog version.
func seedAgentRow(t *testing.T, pool *pgxpool.Pool, id, status string, catalogVersion int64) {
	t.Helper()
	_, err := sqlc.New(pool).CreateAuthzPolicy(context.Background(), sqlc.CreateAuthzPolicyParams{
		ID:             id,
		Name:           id,
		ResourceType:   authz.ResourceAgent.String(),
		Action:         authz.ActionRead.String(),
		Effect:         string(EffectAllow),
		Subjects:       []byte(`{"any":true}`),
		Attributes:     []byte(`{}`),
		CatalogVersion: catalogVersion,
		Status:         status,
	})
	if err != nil {
		t.Fatalf("seed row %s: %v", id, err)
	}
}

// An active row pinned to a different catalog version — lower (stale) or higher
// (future) — cannot be interpreted under current semantics, so the whole
// reload/Begin fails closed as unavailable.
func TestActiveRowWrongCatalogVersionFailsBegin(t *testing.T) {
	ctx := context.Background()
	current := int64(authz.CatalogVersion)

	for _, tc := range []struct {
		name    string
		version int64
	}{
		{"lower version", current - 1},
		{"higher version", current + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := dbtest.New(t)
			seedAgentRow(t, pool, "wrong-version", statusActive, tc.version)

			az := New(pool)
			_, err := az.Begin(ctx, userAuthority(t, "u1", false))
			if !errors.Is(err, authz.ErrAuthorizerUnavailable) {
				t.Fatalf("Begin over version-%d active row = %v, want ErrAuthorizerUnavailable", tc.version, err)
			}
			if !errors.Is(err, ErrCatalogVersion) {
				t.Fatalf("Begin error must wrap ErrCatalogVersion, got %v", err)
			}
		})
	}
}

// A matching current-version active row is interpreted normally, and a
// quarantined legacy catalog_version=0 row stays inert (never reaches reload),
// so Begin succeeds in both cases.
func TestInactiveControlPlanePolicyRowsAreIgnored(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	_, err := sqlc.New(pool).CreateAuthzPolicy(ctx, sqlc.CreateAuthzPolicyParams{
		ID: "legacy-provider", Name: "legacy-provider", ResourceType: authz.ResourceProvider.String(),
		Action: "invalid", Effect: string(EffectAllow), Subjects: []byte(`{}`), Attributes: []byte(`{}`),
		CatalogVersion: 0, Status: statusActive,
	})
	if err != nil {
		t.Fatalf("seed inactive control-plane row: %v", err)
	}
	az := New(pool)
	eval, err := az.Begin(ctx, userAuthority(t, "u1", false))
	if err != nil {
		t.Fatalf("Begin must ignore inactive control-plane policy: %v", err)
	}
	res, err := authz.NewResource(authz.ResourceProvider, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	req, err := authz.NewRequest(authz.ActionRead, res, authz.InvocationFacts{})
	if err != nil {
		t.Fatal(err)
	}
	dec, err := eval.Decide(req)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed() {
		t.Fatal("inactive control-plane custom policy must not grant access")
	}
}

func TestCatalogVersionAcceptsCurrentAndIgnoresQuarantined(t *testing.T) {
	ctx := context.Background()

	t.Run("current version active row loads", func(t *testing.T) {
		pool := dbtest.New(t)
		seedAgentRow(t, pool, "ok", statusActive, int64(authz.CatalogVersion))
		if _, err := New(pool).Begin(ctx, userAuthority(t, "u1", false)); err != nil {
			t.Fatalf("Begin over current-version active row = %v, want success", err)
		}
	})

	t.Run("quarantined v0 row stays inert", func(t *testing.T) {
		pool := dbtest.New(t)
		// A quarantined legacy row with catalog_version 0 must NOT trip the guard,
		// because it is never in the active set.
		seedAgentRow(t, pool, "legacy", statusQuarantined, 0)
		if _, err := New(pool).Begin(ctx, userAuthority(t, "u1", false)); err != nil {
			t.Fatalf("Begin with only a quarantined v0 row = %v, want success", err)
		}
	})
}
