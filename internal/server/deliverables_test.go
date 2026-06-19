package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/deliverable"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// noopExecutor is a no-op deliverable.Executor. The deliverable HTTP handlers
// under test (create / get / add-edge) never reach the executor, so it never
// runs; it exists only to satisfy WithExecutor.
type noopExecutor struct{}

func (noopExecutor) Execute(context.Context, deliverable.ExecutorRequest) (deliverable.ExecutorResult, error) {
	return deliverable.ExecutorResult{}, nil
}

// setupDeliverableEnv boots a deliverable service over env.db and wires it into
// the server. The agent ServiceManager that deliverable.Boot needs is too heavy
// here, so the bundle is constructed directly with a stub session minter that
// seeds a real ctx_conversation row (the session_id FK) — mirroring the
// package's own harness — and a no-op executor.
func setupDeliverableEnv(t *testing.T) *testEnv {
	t.Helper()
	env := setupAdmin(t)

	mint := func(ctx context.Context, userID, agentID, projectID string) (string, error) {
		sessionID := "dlv-" + uuid.NewString()
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		if _, err := env.db.ExecContext(ctx, `
			INSERT INTO ctx_conversation (id, session_id, title, channel, kind, agent_id, user_id, last_active, created_at, updated_at)
			VALUES (?, ?, 'minted', 'task', 'task', ?, ?, ?, ?, ?)`,
			uuid.NewString(), sessionID, agentID, userID, now, now, now); err != nil {
			return "", err
		}
		return sessionID, nil
	}

	q := sqlc.New(env.db)
	svc := deliverable.New(env.db, q,
		deliverable.WithSessionMinter(mint),
		deliverable.WithPlanningSessionMinter(mint),
		deliverable.WithExecutor(noopExecutor{}),
	)
	bundle := &deliverable.Service{Queries: q, Deliverable: svc}
	env.srv.SetDeliverableService(bundle)
	return env
}

// createDeliverable POSTs a leaf deliverable for the given bearer token and
// returns its id, failing the test on a non-201.
func createDeliverable(t *testing.T, env *testEnv, token, agentID, title string) string {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, token, "POST", "/api/deliverables", apitypes.CreateDeliverableRequest{
		AgentId: agentID,
		Title:   title,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create deliverable %q: status = %d, want %d (body: %s)", title, rr.Code, http.StatusCreated, rr.Body.String())
	}
	var d apitypes.Deliverable
	if err := json.Unmarshal(parseResponse(t, rr).Data, &d); err != nil {
		t.Fatalf("unmarshal created deliverable: %v", err)
	}
	if d.Id == "" {
		t.Fatalf("create deliverable %q: empty id (body: %s)", title, rr.Body.String())
	}
	return d.Id
}

// TestDeliverables_AddEdge_CrossTenant_404 is the IDOR regression: the AddEdge
// handler gates the caller-supplied upstream through the same ownership check as
// the downstream. Without that gate, user A could wire user B's deliverable as an
// upstream dependency and pull B's frozen accepted_output into A's attempt input.
func TestDeliverables_AddEdge_CrossTenant_404(t *testing.T) {
	env := setupDeliverableEnv(t)
	agentID := findStellaID(t, env)

	// User B + token. Reuse the same agent — there is no per-user agent ownership
	// gate at create time, so the only tenant boundary exercised here is the
	// deliverable's user_id.
	_, tokenB := createTestUserWithToken(t, env.authStore, env.oidcStore, "tenant-b", "user")

	// A's two own deliverables and B's one.
	dA := createDeliverable(t, env, env.bearerToken, agentID, "A-root")
	dA2 := createDeliverable(t, env, env.bearerToken, agentID, "A-sibling")
	dB := createDeliverable(t, env, tokenB, agentID, "B-root")

	// As A, declare dA depends on B's deliverable as upstream → 404 (upstream gated).
	rr := doRequestWithSession(t, env.srv, env.bearerToken, "POST", "/api/deliverables/"+dA+"/edges", apitypes.AddEdgeRequest{
		UpstreamId: dB,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("A edge upstream=B: status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	if got := parseResponse(t, rr).Error; got != "not_found" {
		t.Fatalf("A edge upstream=B: error = %q, want %q", got, "not_found")
	}

	// Reverse: as B, declare dB depends on A's deliverable (the downstream is B's
	// own, the upstream is A's) → the downstream loads, the upstream gate fails → 404.
	rr = doRequestWithSession(t, env.srv, tokenB, "POST", "/api/deliverables/"+dB+"/edges", apitypes.AddEdgeRequest{
		UpstreamId: dA,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("B edge upstream=A: status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}

	// Cross-tenant downstream: as A, target B's deliverable as the path id → the
	// downstream gate fails first → 404.
	rr = doRequestWithSession(t, env.srv, env.bearerToken, "POST", "/api/deliverables/"+dB+"/edges", apitypes.AddEdgeRequest{
		UpstreamId: dA,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("A edge downstream=B: status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}

	// Same-tenant edge between A's two own siblings → 201.
	rr = doRequestWithSession(t, env.srv, env.bearerToken, "POST", "/api/deliverables/"+dA+"/edges", apitypes.AddEdgeRequest{
		UpstreamId: dA2,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("A edge upstream=A-sibling: status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}
	var edge apitypes.Edge
	if err := json.Unmarshal(parseResponse(t, rr).Data, &edge); err != nil {
		t.Fatalf("unmarshal edge: %v", err)
	}
	if edge.DeliverableId != dA || edge.UpstreamId != dA2 {
		t.Fatalf("edge = {downstream:%q upstream:%q}, want {%q %q}", edge.DeliverableId, edge.UpstreamId, dA, dA2)
	}
}

// TestDeliverables_GetCrossTenant_404 proves a deliverable owned by another
// tenant is reported as not-found (existence is not leaked).
func TestDeliverables_GetCrossTenant_404(t *testing.T) {
	env := setupDeliverableEnv(t)
	agentID := findStellaID(t, env)

	_, tokenB := createTestUserWithToken(t, env.authStore, env.oidcStore, "tenant-b-get", "user")
	dB := createDeliverable(t, env, tokenB, agentID, "B-private")

	rr := doRequestWithSession(t, env.srv, env.bearerToken, "GET", "/api/deliverables/"+dB, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("A GET B's deliverable: status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	if got := parseResponse(t, rr).Error; got != "not_found" {
		t.Fatalf("A GET B's deliverable: error = %q, want %q", got, "not_found")
	}
}

// TestDeliverables_CreateAndGet covers the happy path: POST creates a leaf and
// GET returns it for the owner.
func TestDeliverables_CreateAndGet(t *testing.T) {
	env := setupDeliverableEnv(t)
	agentID := findStellaID(t, env)

	id := createDeliverable(t, env, env.bearerToken, agentID, "my-goal")

	rr := doRequestWithSession(t, env.srv, env.bearerToken, "GET", "/api/deliverables/"+id, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET own deliverable: status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var d apitypes.Deliverable
	if err := json.Unmarshal(parseResponse(t, rr).Data, &d); err != nil {
		t.Fatalf("unmarshal deliverable: %v", err)
	}
	if d.Id != id {
		t.Fatalf("GET deliverable id = %q, want %q", d.Id, id)
	}
	if d.UserId != env.adminUser.ID {
		t.Fatalf("GET deliverable user_id = %q, want %q", d.UserId, env.adminUser.ID)
	}
	if d.Title != "my-goal" {
		t.Fatalf("GET deliverable title = %q, want %q", d.Title, "my-goal")
	}
	if d.Kind != apitypes.DeliverableKindLeaf {
		t.Fatalf("GET deliverable kind = %q, want %q", d.Kind, apitypes.DeliverableKindLeaf)
	}
}
