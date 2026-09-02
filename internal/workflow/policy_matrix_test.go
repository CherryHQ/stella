package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/goal"
	storepkg "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestWorkflowMissingDomainDependenciesFailClosed(t *testing.T) {
	pool := dbtest.New(t)
	authority := workflowUserAuthority(t, uuid.NewString(), false)

	withoutAgents := New(pool, nil, nil)
	acc, err := withoutAgents.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.Instantiate(context.Background(), "missing", nil, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Instantiate without Agent access = %v, want unavailable", err)
	}
	if _, err := acc.SaveGoalAsWorkflow(context.Background(), SaveInput{GoalID: "missing"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SaveGoalAsWorkflow without Agent access = %v, want unavailable", err)
	}

	store := storepkg.NewDBStore(pool)
	assign := appdb.NewAuthStore(pool)
	withoutGoals := New(pool, nil, agentaccess.NewService(store, assign))
	acc, err = withoutGoals.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acc.SaveGoalAsWorkflow(context.Background(), SaveInput{GoalID: "missing"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SaveGoalAsWorkflow without Goal access = %v, want unavailable", err)
	}
}

func TestEmbeddedPostgresWorkflowDirectAccess(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := storepkg.NewDBStore(pool)
	oidc := appdb.NewOIDCStore(pool)
	assign := appdb.NewAuthStore(pool)
	q := sqlc.New(pool)

	owner := createWorkflowUser(t, ctx, oidc, "owner")
	other := createWorkflowUser(t, ctx, oidc, "other")
	for _, agent := range []config.Agent{
		{ID: "owned", Name: "owned", Model: "p/m", Workspace: "/tmp/owned", Scope: config.AgentScopeRestricted, CreatorID: owner.ID, Enabled: true},
		{ID: "wrong", Name: "wrong", Model: "p/m", Workspace: "/tmp/wrong", Scope: config.AgentScopeRestricted, CreatorID: owner.ID, Enabled: true},
	} {
		if err := store.CreateAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := assign.AssignAgent(ctx, owner.ID, "owned"); err != nil {
		t.Fatal(err)
	}

	wf := createWorkflow(t, q, owner.ID, "owned")
	svc := New(pool, nil, agentaccess.NewService(store, assign))
	ownerAuth := workflowUserAuthority(t, owner.ID, false)
	adminAuth := workflowUserAuthority(t, uuid.NewString(), true)
	foreignAuth := workflowUserAuthority(t, other.ID, false)
	ownerAgent := workflowAgentAuthority(t, owner.ID, "owned")
	wrongAgent := workflowAgentAuthority(t, owner.ID, "wrong")
	foreignAgent := workflowAgentAuthority(t, other.ID, "owned")

	for _, tc := range []struct {
		name      string
		authority authz.Authority
		action    authz.Action
		wantErr   error
	}{
		{"owner reads", ownerAuth, authz.ActionRead, nil},
		{"owner writes", ownerAuth, authz.ActionWrite, nil},
		{"owner deletes", ownerAuth, authz.ActionDelete, nil},
		{"owner executes", ownerAuth, authz.ActionExecute, nil},
		{"admin reads foreign durable workflow", adminAuth, authz.ActionRead, nil},
		{"admin has full workflow actions", adminAuth, authz.ActionManage, nil},
		{"foreign user cannot read", foreignAuth, authz.ActionRead, ErrNotFound},
		{"executor reads", ownerAgent, authz.ActionRead, nil},
		{"executor creates", ownerAgent, authz.ActionCreate, nil},
		{"executor executes", ownerAgent, authz.ActionExecute, nil},
		{"executor cannot write", ownerAgent, authz.ActionWrite, ErrNotFound},
		{"executor cannot delete", ownerAgent, authz.ActionDelete, ErrNotFound},
		{"wrong executor cannot read", wrongAgent, authz.ActionRead, ErrNotFound},
		{"foreign executor cannot execute", foreignAgent, authz.ActionExecute, ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc, err := svc.Begin(ctx, tc.authority)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			err = acc.authorize(tc.action, wf)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("authorize(%s) = %v, want %v", tc.action, err, tc.wantErr)
			}
		})
	}

	for _, tc := range []struct {
		name      string
		authority authz.Authority
		want      int
	}{
		{"owner list", ownerAuth, 1},
		{"foreign user list", foreignAuth, 0},
		// Listing has always been user-scoped. Admin's per-row full access does
		// not expand this collection query to every owner's workflows.
		{"admin list stays owner scoped", adminAuth, 0},
		{"executor list", ownerAgent, 1},
		{"wrong executor list", wrongAgent, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc, err := svc.Begin(ctx, tc.authority)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := acc.List(ctx, "")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(rows) != tc.want {
				t.Fatalf("List = %d rows, want %d", len(rows), tc.want)
			}
		})
	}

	acc, err := svc.Begin(ctx, wrongAgent)
	if err != nil {
		t.Fatal(err)
	}
	if err := acc.Delete(ctx, wf.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong executor Delete = %v, want not found", err)
	}
	if _, err := svc.Begin(ctx, authz.Authority{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("invalid authority Begin = %v, want forbidden", err)
	}
	for _, authority := range []authz.Authority{
		workflowGroupAuthority(t),
		workflowSystemAuthority(t),
	} {
		acc, err := svc.Begin(ctx, authority)
		if err != nil {
			t.Fatal(err)
		}
		if err := acc.authorize(authz.ActionRead, wf); !errors.Is(err, ErrNotFound) {
			t.Fatalf("non-workflow authority authorize = %v, want not found", err)
		}
	}
}

// TestWorkflowInstantiateAsChecksPersistedWorkflowAndAgent proves authorization
// happens before the claim, and idempotency replays only through the same durable
// Workflow and Agent checks.
func TestWorkflowInstantiateAsChecksPersistedWorkflowAndAgent(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := storepkg.NewDBStore(pool)
	oidc := appdb.NewOIDCStore(pool)
	assign := appdb.NewAuthStore(pool)
	q := sqlc.New(pool)
	owner := createWorkflowUser(t, ctx, oidc, "instantiate")
	if err := store.CreateAgent(ctx, config.Agent{ID: "owned", Name: "owned", Model: "p/m", Workspace: "/tmp/owned", Scope: config.AgentScopeRestricted, CreatorID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := assign.AssignAgent(ctx, owner.ID, "owned"); err != nil {
		t.Fatal(err)
	}
	wf := createWorkflow(t, q, owner.ID, "owned")
	goals := goal.New(pool, q)
	svc := New(pool, goal.NewBundle(q, goals, nil).WorkflowWriter(), agentaccess.NewService(store, assign))

	foreign := workflowUserAuthority(t, uuid.NewString(), false)
	if _, _, err := svc.InstantiateAs(ctx, foreign, wf.ID, nil, "same"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign InstantiateAs = %v, want not found", err)
	}
	ownerAuth := workflowUserAuthority(t, owner.ID, false)
	first, created, err := svc.InstantiateAs(ctx, ownerAuth, wf.ID, nil, "same")
	if err != nil || !created {
		t.Fatalf("first InstantiateAs = (%+v, %v, %v), want created run", first, created, err)
	}
	second, created, err := svc.InstantiateAs(ctx, ownerAuth, wf.ID, nil, "same")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("idempotent InstantiateAs = (%+v, %v, %v), want replay of %q", second, created, err, first.ID)
	}
	admin := workflowUserAuthority(t, uuid.NewString(), true)
	third, created, err := svc.InstantiateAs(ctx, admin, wf.ID, nil, "same")
	if err != nil || created || third.ID != first.ID {
		t.Fatalf("admin InstantiateAs replay = (%+v, %v, %v), want replay of %q", third, created, err, first.ID)
	}
}

// TestSaveGoalAsWorkflowAuthorizesSourceGoal proves the source Goal is read
// through Goal's direct port, the request cannot choose its target agent, and the
// target Agent must remain executable.
func TestSaveGoalAsWorkflowAuthorizesSourceGoal(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := storepkg.NewDBStore(pool)
	oidc := appdb.NewOIDCStore(pool)
	assign := appdb.NewAuthStore(pool)
	q := sqlc.New(pool)

	owner := createWorkflowUser(t, ctx, oidc, "save-owner")
	other := createWorkflowUser(t, ctx, oidc, "save-other")
	for _, agent := range []config.Agent{
		{ID: "owned", Name: "owned", Model: "p/m", Workspace: "/tmp/owned", Scope: config.AgentScopeRestricted, CreatorID: owner.ID, Enabled: true},
		{ID: "unassigned", Name: "unassigned", Model: "p/m", Workspace: "/tmp/unassigned", Scope: config.AgentScopeRestricted, CreatorID: owner.ID, Enabled: true},
	} {
		if err := store.CreateAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := assign.AssignAgent(ctx, owner.ID, "owned"); err != nil {
		t.Fatal(err)
	}
	goals := goal.New(pool, q)
	root := createAcceptedWorkflowGoal(t, ctx, pool, goals, owner.ID, "owned")
	unassignedRoot := createAcceptedWorkflowGoal(t, ctx, pool, goals, owner.ID, "unassigned")
	svc := New(pool, goal.NewBundle(q, goals, nil).WorkflowWriter(), agentaccess.NewService(store, assign))

	ownerAcc, err := svc.Begin(ctx, workflowUserAuthority(t, owner.ID, false))
	if err != nil {
		t.Fatal(err)
	}
	wf, err := ownerAcc.SaveGoalAsWorkflow(ctx, SaveInput{UserID: other.ID, AgentID: "spoofed", GoalID: root.ID, Name: "wf"})
	if err != nil {
		t.Fatalf("owner SaveGoalAsWorkflow: %v", err)
	}
	if derefString(wf.UserID) != owner.ID || derefString(wf.AgentID) != "owned" {
		t.Fatalf("workflow owner/agent = %q/%q, want durable source owner target %q/%q", derefString(wf.UserID), derefString(wf.AgentID), owner.ID, "owned")
	}
	if _, err := ownerAcc.SaveGoalAsWorkflow(ctx, SaveInput{GoalID: unassignedRoot.ID, Name: "unassigned"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unexecutable target SaveGoalAsWorkflow = %v, want not found", err)
	}

	foreignAcc, err := svc.Begin(ctx, workflowUserAuthority(t, other.ID, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreignAcc.SaveGoalAsWorkflow(ctx, SaveInput{GoalID: root.ID, Name: "wf-foreign"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign SaveGoalAsWorkflow = %v, want ErrNotFound", err)
	}
}

func workflowUserAuthority(t *testing.T, id string, admin bool) authz.Authority {
	t.Helper()
	authority, err := authz.NewUserAuthority(authz.UserID(id), admin)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func workflowGroupAuthority(t *testing.T) authz.Authority {
	t.Helper()
	authority, err := authz.NewGroupAgentAuthority("workflow-test", "workflow-agent")
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func workflowSystemAuthority(t *testing.T) authz.Authority {
	t.Helper()
	authority, err := authz.NewSystemAuthority("workflow-test")
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func workflowAgentAuthority(t *testing.T, userID, agentID string) authz.Authority {
	t.Helper()
	authority, err := agentaccess.WorkerAgentAuthority(userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func createWorkflowUser(t *testing.T, ctx context.Context, oidc *appdb.OIDCStore, prefix string) auth.User {
	t.Helper()
	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: prefix + "@wf.test", Name: prefix, Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func createAcceptedWorkflowGoal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, goals *goal.GoalService, userID, agentID string) sqlc.AgentGoal {
	t.Helper()
	root, err := goals.CreateRoot(ctx, goal.CreateInput{UserID: userID, AgentID: agentID, Title: "t", Intent: "i", Kind: goal.KindComposite, Required: true})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	plan := goal.DecompositionContent{Children: []goal.ProposedChild{{Key: "leaf", Title: "Leaf", Intent: "do leaf", Kind: goal.KindLeaf, Required: true}}}
	if err := goals.MaterializeFrozenLayer(ctx, root.ID, plan, goal.FrozenStamp{}); err != nil {
		t.Fatalf("materialize plan: %v", err)
	}
	if err := goals.ActivateFrozenComposite(ctx, root.ID); err != nil {
		t.Fatalf("activate root: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_goal SET lifecycle='done', done_reason='accepted', acceptance_state='passed', accepted_output='{}', accepted_at=now() WHERE id=$1`, root.ID); err != nil {
		t.Fatalf("force accept: %v", err)
	}
	return root
}

func createWorkflow(t *testing.T, q *sqlc.Queries, userID, agentID string) sqlc.AgentWorkflow {
	t.Helper()
	payload, err := json.Marshal(FrozenPlan{Children: []FrozenNode{{Child: goal.ProposedChild{
		Key: "leaf", Title: "Leaf", Intent: "do leaf", Kind: goal.KindLeaf, Required: true,
	}}}})
	if err != nil {
		t.Fatal(err)
	}
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
		Payload:            payload,
		FullyFrozen:        true,
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	return wf
}
