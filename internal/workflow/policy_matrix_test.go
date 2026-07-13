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
	"github.com/CherryHQ/stella/internal/goal"
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

func TestWorkflowMissingAgentPEPFailsClosed(t *testing.T) {
	pool := dbtest.New(t)
	svc := New(pool, nil, policy.New(pool), nil)
	rss, err := authz.NewRoleSet(authz.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(uuid.NewString()), rss, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	acc, err := svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.Instantiate(context.Background(), "missing", nil, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Instantiate missing Agent PEP err=%v, want unavailable", err)
	}
	if _, err := acc.SaveGoalAsWorkflow(context.Background(), SaveInput{GoalID: "missing"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SaveGoalAsWorkflow missing Agent PEP err=%v, want unavailable", err)
	}
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

// TestSaveGoalAsWorkflowAuthorizesSourceGoal proves SaveGoalAsWorkflow decides the
// source goal read and the target workflow create under one evaluation: the owner
// succeeds (and the workflow binds to the goal's persisted agent), while a foreign
// user and a custom deny on the goal read are both hidden as ErrNotFound.
func TestSaveGoalAsWorkflowAuthorizesSourceGoal(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := storepkg.NewDBStore(pool)
	oidc := appdb.NewOIDCStore(pool)
	assign := appdb.NewAuthStore(pool)
	q := sqlc.New(pool)

	owner, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "owner@save.test", Name: "owner", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	other, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "other@save.test", Name: "other", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAgent(ctx, config.Agent{ID: "owned", Name: "owned", Model: "p/m", Workspace: "/tmp/owned", Scope: config.AgentScopeRestricted, CreatorID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := assign.AssignAgent(ctx, owner.ID, "owned"); err != nil {
		t.Fatal(err)
	}

	goals := goal.New(pool, q)
	root, err := goals.CreateRoot(ctx, goal.CreateInput{UserID: owner.ID, AgentID: "owned", Title: "t", Intent: "i", Kind: goal.KindComposite, Required: true})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	// A savable workflow needs an accepted root composite with a materialized plan.
	rootPlan := goal.DecompositionContent{Children: []goal.ProposedChild{
		{Key: "leaf", Title: "Leaf", Intent: "do leaf", Kind: goal.KindLeaf, Required: true},
	}}
	if err := goals.MaterializeFrozenLayer(ctx, root.ID, rootPlan, goal.FrozenStamp{}); err != nil {
		t.Fatalf("materialize plan: %v", err)
	}
	if err := goals.ActivateFrozenComposite(ctx, root.ID); err != nil {
		t.Fatalf("activate root: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_goal SET lifecycle='done', done_reason='accepted', acceptance_state='passed', accepted_output='{}', accepted_at=now() WHERE id=$1`, root.ID); err != nil {
		t.Fatalf("force accept: %v", err)
	}

	az := &countingAuthorizer{Authorizer: policy.New(pool)}
	svc := New(pool, goals, az, agentaccess.NewService(store, assign, az))

	userAuth := func(id string) authz.Authority {
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

	ownerAcc, err := svc.Begin(ctx, userAuth(owner.ID))
	if err != nil {
		t.Fatal(err)
	}
	wf, err := ownerAcc.SaveGoalAsWorkflow(ctx, SaveInput{GoalID: root.ID, Name: "wf"})
	if err != nil {
		t.Fatalf("owner SaveGoalAsWorkflow: %v", err)
	}
	if wf.AgentID.String != "owned" {
		t.Fatalf("workflow agent = %q, want owned (from source goal)", wf.AgentID.String)
	}

	foreignAcc, err := svc.Begin(ctx, userAuth(other.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreignAcc.SaveGoalAsWorkflow(ctx, SaveInput{GoalID: root.ID, Name: "wf-foreign"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign SaveGoalAsWorkflow = %v, want ErrNotFound", err)
	}

	ps := policy.NewService(policy.New(pool))
	if _, _, err := ps.CreatePolicy(ctx, policy.PolicyInput{
		Name: "deny own goal read", Resource: authz.ResourceGoal, Action: authz.ActionRead,
		Effect: policy.EffectDeny, Subjects: policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
		Predicates: []policy.Predicate{policy.Eq("is_owner", "true")},
	}); err != nil {
		t.Fatal(err)
	}
	denyAcc, err := svc.Begin(ctx, userAuth(owner.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := denyAcc.SaveGoalAsWorkflow(ctx, SaveInput{GoalID: root.ID, Name: "wf-denied"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("goal-read-denied SaveGoalAsWorkflow = %v, want ErrNotFound", err)
	}
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
