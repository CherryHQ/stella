package agentaccess

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	storepkg "github.com/CherryHQ/stella/internal/store"
)

// countingAuthorizer proves the PEP, rather than a fake evaluator, owns exactly
// one Begin for each durable use case.
type countingAuthorizer struct {
	authz.Authorizer
	begins int
}

func (a *countingAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	a.begins++
	return a.Authorizer.Begin(ctx, authority)
}

func TestEmbeddedPostgresAgentPolicyMatrix(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := storepkg.NewDBStore(pool)
	oidc := appdb.NewOIDCStore(pool)
	assign := appdb.NewAuthStore(pool)
	owner, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "owner@example.test", Name: "owner", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	other, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "other@example.test", Name: "other", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []config.Agent{
		{ID: "system", Name: "system", Model: "p/m", Workspace: "/tmp/system", Scope: config.AgentScopeSystem, CreatorID: owner.ID, Enabled: true},
		{ID: "owned", Name: "owned", Model: "p/m", Workspace: "/tmp/owned", Scope: config.AgentScopeRestricted, CreatorID: owner.ID, Enabled: true},
	} {
		if err := store.CreateAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := assign.AssignAgent(ctx, owner.ID, "owned"); err != nil {
		t.Fatal(err)
	}

	az := &countingAuthorizer{Authorizer: policy.New(pool)}
	access := NewService(store, assign, az)
	user := func(id string) authz.Authority {
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

	cases := []struct {
		name      string
		authority authz.Authority
		agentID   string
		wantErr   error
	}{
		{"user can use system", user(owner.ID), "system", nil},
		{"user can use assigned", user(owner.ID), "owned", nil},
		{"other cannot use private", user(other.ID), "owned", ErrForbidden},
		{"durable worker exact executor", mustWorkerAuthority(t, owner.ID, "owned"), "owned", nil},
		{"durable worker wrong executor", mustWorkerAuthority(t, owner.ID, "system"), "owned", ErrForbidden},
		{"named maintenance exact grant", mustSystemAuthority(t), "owned", nil},
		{"group exact member", mustGroupAuthority(t, "g1", "owned"), "owned", nil},
		{"group cannot switch agent", mustGroupAuthority(t, "g1", "system"), "owned", ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := az.begins
			_, err := access.Use(ctx, tc.authority, tc.agentID)
			if tc.wantErr == nil && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if az.begins != before+1 {
				t.Fatalf("Begin count = %d, want %d", az.begins-before, 1)
			}
		})
	}

	// Active custom deny overrides built-ins against the persisted facts. The
	// PEP returns its forbidden visibility, not a made-up success/not-found.
	ps := policy.NewService(policy.New(pool))
	if _, _, err := ps.CreatePolicy(ctx, policy.PolicyInput{
		Name: "deny system use", Resource: authz.ResourceAgent, Action: authz.ActionExecute,
		Effect: policy.EffectDeny, Subjects: policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
		Predicates: []policy.Predicate{policy.Eq("scope", "system")},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = access.Use(ctx, user(owner.ID), "system")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("custom deny error = %v, want forbidden", err)
	}
}

func mustWorkerAuthority(t *testing.T, owner, executor string) authz.Authority {
	t.Helper()
	a, err := WorkerAgentAuthority(owner, executor)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustSystemAuthority(t *testing.T) authz.Authority {
	t.Helper()
	a, err := SystemAgentAuthority("test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustGroupAuthority(t *testing.T, group, executor string) authz.Authority {
	t.Helper()
	a, err := GroupAgentAuthority(group, executor)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
