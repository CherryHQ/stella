package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/server"
	workflowpkg "github.com/CherryHQ/stella/internal/workflow"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func setupWorkflowEnv(t *testing.T) *testEnv {
	t.Helper()
	env := setupAdmin(t)
	env.rebuild(t, func(d *server.Deps) {
		d.Workflow = workflowpkg.New(env.db, nil, policy.New(env.db), d.AgentAccess)
	})
	return env
}

func createWorkflowRow(t *testing.T, env *testEnv, agentID string) sqlc.AgentWorkflow {
	t.Helper()
	q := sqlc.New(env.db)
	wf, err := q.CreateWorkflow(context.Background(), sqlc.CreateWorkflowParams{
		ID:                 uuid.NewString(),
		OwnerKind:          workflowpkg.OwnerAgent,
		UserID:             pgnull.Text(env.adminUser.ID),
		AgentID:            pgnull.Text(agentID),
		Name:               "daily brief",
		Version:            1,
		Intent:             "brief the user",
		AcceptanceContract: []byte(`{}`),
		ConvergencePolicy:  []byte(`{}`),
		Inputs:             []byte(`[{"name":"topic","required":true}]`),
		PayloadFormat:      workflowpkg.PayloadFormatFrozenV0,
		Payload:            []byte(`{"children":[],"edges":[]}`),
		FullyFrozen:        true,
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	return wf
}

func TestWorkflows_ListGetAndDeleteConflict(t *testing.T) {
	env := setupWorkflowEnv(t)
	agentID := findStellaID(t, env)
	wf := createWorkflowRow(t, env, agentID)

	listRR := doRequestWithSession(t, env.srv, env.bearerToken, "GET", "/api/workflows?agent_id="+agentID, nil)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list workflows: status = %d, body: %s", listRR.Code, listRR.Body.String())
	}
	var list apitypes.WorkflowList
	if err := json.Unmarshal(parseResponse(t, listRR).Data, &list); err != nil {
		t.Fatalf("unmarshal workflow list: %v", err)
	}
	if len(list.Workflows) != 1 || list.Workflows[0].Id != wf.ID || list.Workflows[0].Inputs[0].Name != "topic" {
		t.Fatalf("unexpected workflow list: %+v", list.Workflows)
	}

	getRR := doRequestWithSession(t, env.srv, env.bearerToken, "GET", "/api/workflows/"+wf.ID, nil)
	if getRR.Code != http.StatusOK {
		t.Fatalf("get workflow: status = %d, body: %s", getRR.Code, getRR.Body.String())
	}

	_, err := sqlc.New(env.db).ClaimWorkflowRun(context.Background(), sqlc.ClaimWorkflowRunParams{ID: uuid.NewString(), WorkflowID: wf.ID, WorkflowVersion: wf.Version, IdempotencyKey: "test", Status: workflowpkg.RunDone, Inputs: []byte(`{}`), PlanHash: "hash"})
	if err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	deleteRR := doRequestWithSession(t, env.srv, env.bearerToken, "DELETE", "/api/workflows/"+wf.ID, nil)
	if deleteRR.Code != http.StatusConflict {
		t.Fatalf("delete workflow with runs: status = %d, want 409 (body: %s)", deleteRR.Code, deleteRR.Body.String())
	}
}
