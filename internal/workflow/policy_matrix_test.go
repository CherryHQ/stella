package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	storepkg "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// countingAuthorizer proves the PEP owns exactly one Begin per use case.
type countingAuthorizer struct {
	authz.Authorizer
	begins int
}

func (a *countingAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	a.begins++
	return a.Authorizer.Begin(ctx, authority)
}

func TestEmbeddedPostgresWorkflowPolicyMatrix(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := storepkg.NewDBStore(pool)
	oidc := appdb.NewOIDCStore(pool)
	assign := appdb.NewAuthStore(pool)
	q := sqlc.New(pool)

	owner, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "owner@wf.test", Name: "owner", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	other, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "other@wf.test", Name: "other", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []config.Agent{
		{ID: "sys", Name: "sys", Model: "p/m", Workspace: "/tmp/sys", Scope: config.AgentScopeSystem, CreatorID: owner.ID, Enabled: true},
		{ID: "owned", Name: "owned", Model: "p/m", Workspace: "/tmp/owned", Scope: config.AgentScopeRestricted, CreatorID: owner.ID, Enabled: true},
	} {
		if err := store.CreateAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := assign.AssignAgent(ctx, owner.ID, "owned"); err != nil {
		t.Fatal(err)
	}

	// A workflow owned by `owner`, bound to the `owned` agent.
	wf := createWorkflow(t, q, owner.ID, "owned")

	az := &countingAuthorizer{Authorizer: policy.New(pool)}
	svc := New(pool, nil, az, agentaccess.NewService(store, assign, az))

	userAuth := func(id string, admin bool) authz.Authority {
		role := authz.RoleUser
		if admin {
			role = authz.RoleAdmin
		}
		rs, err := authz.NewRoleSet(role)
		if err != nil {
			t.Fatal(err)
		}
		a, err := authz.NewUserAuthority(authz.UserID(id), rs, authz.GrantSet{})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	agentAuth := func(userID, agentID string) authz.Authority {
		a, err := agentaccess.WorkerAgentAuthority(userID, agentID)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	cases := []struct {
		name      string
		authority authz.Authority
		wantErr   error
	}{
		{"owner reads own workflow", userAuth(owner.ID, false), nil},
		{"foreign user cannot read", userAuth(other.ID, false), ErrNotFound},
		{"admin reads any workflow", userAuth("admin-x", true), nil},
		{"delegated executor reads", agentAuth(owner.ID, "owned"), nil},
		{"delegated wrong executor denied", agentAuth(owner.ID, "sys"), ErrNotFound},
		{"foreign delegated denied", agentAuth(other.ID, "owned"), ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := az.begins
			acc, err := svc.Begin(ctx, tc.authority)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			_, err = acc.Get(ctx, wf.ID)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("Get = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Get = %v, want %v", err, tc.wantErr)
			}
			if az.begins != before+1 {
				t.Fatalf("Begin count = %d, want 1", az.begins-before)
			}
		})
	}

	// A per-row list decision uses the same revision as the collection gate: the
	// owner sees exactly their workflow; a foreign user sees none.
	t.Run("list filters per row", func(t *testing.T) {
		acc, err := svc.Begin(ctx, userAuth(owner.ID, false))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := acc.List(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ID != wf.ID {
			t.Fatalf("owner list = %d rows, want 1 (own)", len(rows))
		}
		facc, err := svc.Begin(ctx, userAuth(other.ID, false))
		if err != nil {
			t.Fatal(err)
		}
		frows, err := facc.List(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(frows) != 0 {
			t.Fatalf("foreign list = %d rows, want 0", len(frows))
		}
	})

	// An active custom deny overrides the owner built-in against the durable facts.
	t.Run("custom deny hides own workflow", func(t *testing.T) {
		ps := policy.NewService(policy.New(pool))
		if _, _, err := ps.CreatePolicy(ctx, policy.PolicyInput{
			Name: "deny own workflow read", Resource: authz.ResourceWorkflow, Action: authz.ActionRead,
			Effect: policy.EffectDeny, Subjects: policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
			Predicates: []policy.Predicate{policy.Eq("is_owner", "true")},
		}); err != nil {
			t.Fatal(err)
		}
		acc, err := svc.Begin(ctx, userAuth(owner.ID, false))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := acc.Get(ctx, wf.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("custom deny Get = %v, want ErrNotFound", err)
		}
	})
}

func createWorkflow(t *testing.T, q *sqlc.Queries, userID, agentID string) sqlc.AgentWorkflow {
	t.Helper()
	wf, err := q.CreateWorkflow(context.Background(), sqlc.CreateWorkflowParams{
		ID:                 uuid.NewString(),
		OwnerKind:          OwnerAgent,
		UserID:             pgnull.Text(userID),
		AgentID:            pgnull.Text(agentID),
		Name:               "brief",
		Version:            1,
		Intent:             "brief",
		AcceptanceContract: []byte(`{}`),
		ConvergencePolicy:  []byte(`{}`),
		Inputs:             []byte(`[]`),
		PayloadFormat:      PayloadFormatFrozenV0,
		Payload:            []byte(`{"children":[],"edges":[]}`),
		FullyFrozen:        true,
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	return wf
}
