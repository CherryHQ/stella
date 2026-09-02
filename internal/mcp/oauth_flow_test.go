package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// seedOAuthRegistration inserts an oauth registration straight through SQL so
// the loopback URL skips validateEndpointURL (which only allows public hosts).
func seedOAuthRegistration(t *testing.T, pool *pgxpool.Pool, scope, userID, agentID, rawURL string) Registration {
	t.Helper()
	q := sqlc.New(pool)
	id := uuid.NewString()
	if _, err := q.CreateMCPServer(context.Background(), sqlc.CreateMCPServerParams{
		ID: id, Scope: scope, UserID: pgtype.Text{String: userID, Valid: userID != ""},
		AgentID: pgtype.Text{String: agentID, Valid: agentID != ""},
		Name:    "oauth-" + id[:8], Url: rawURL, Transport: TransportStreamableHTTP,
		AuthType: AuthTypeOAuth, Enabled: true, Metadata: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("seed oauth registration: %v", err)
	}
	return registrationFromRow(mustGetRow(t, pool, id))
}

func mustGetRow(t *testing.T, pool *pgxpool.Pool, id string) sqlc.McpServer {
	t.Helper()
	row, err := sqlc.New(pool).GetMCPServerByID(context.Background(), id)
	if err != nil {
		t.Fatalf("get seeded row: %v", err)
	}
	return row
}

// startFlow drives StartOAuth against the fakes and pins the PKCE challenge on
// the fake AS so /token can validate the verifier end to end.
func startFlow(t *testing.T, svc *Service, as *fakeAS, reg Registration, userID string) (flowID string) {
	t.Helper()
	authURL, flowID, _, err := svc.StartOAuth(context.Background(), reg, userID, "http://192.0.2.10/api/mcp/oauth/callback")
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	_, challenge, resource := parseAuthCodeURL(t, authURL)
	if resource == "" {
		t.Fatal("authorization URL is missing the resource parameter")
	}
	as.setExpectedChallenge(challenge)
	return flowID
}

func TestOAuthFlowEndToEnd(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userID, _ := setupInternal(t)
	as := newFakeAS(t)
	mcpSrv := fakeMCPServer(t, as.ts.URL)
	as.resource = mcpSrv.URL
	reg := seedOAuthRegistration(t, svc.pool, ScopeUser, userID, "", mcpSrv.URL)
	svc.SetConnectForTesting(func(context.Context, Registration, CredentialOwner) (RemoteClient, error) {
		return &fakeMCPClient{tools: []*mcpsdk.Tool{{Name: "list", InputSchema: map[string]any{"type": "object"}}}}, nil
	})

	flowID := startFlow(t, svc, as, reg, userID)

	updated, err := svc.CompleteOAuth(context.Background(), flowID, "auth-code")
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	if updated.Status != StatusOK {
		t.Fatalf("status = %q, want ok after the post-connect probe", updated.Status)
	}

	// The bundle landed in the vault at the registration's own tuple...
	bundle, err := svc.loadBundle(context.Background(), reg, svc.CredentialOwner(reg, userID))
	if err != nil || bundle == nil {
		t.Fatalf("loadBundle: %v, %v", bundle, err)
	}
	if bundle.AccessToken != "new-access" || bundle.RefreshToken != "new-refresh" {
		t.Fatalf("bundle tokens = %q/%q", bundle.AccessToken, bundle.RefreshToken)
	}
	if bundle.GrantedScope != "mcp:read" {
		t.Fatalf("granted scope = %q", bundle.GrantedScope)
	}
	// ...and the client identity was persisted so DCR runs once.
	fresh := registrationFromRow(mustGetRow(t, svc.pool, reg.ID))
	if fresh.OAuthClientID == "" {
		t.Fatal("DCR client id was not persisted into metadata")
	}
	if n := len(as.clients); n != 1 {
		t.Fatalf("DCR calls = %d, want 1", n)
	}

	// Replay: the flow is one-shot; a second callback must fail and must not
	// write a second bundle.
	if _, err := svc.CompleteOAuth(context.Background(), flowID, "auth-code"); err == nil {
		t.Fatal("replayed callback must fail")
	}
	if got := bundle.AccessToken; got != "new-access" {
		t.Fatalf("bundle mutated by replay: %q", got)
	}
}

func TestOAuthFlowExpired(t *testing.T) {
	svc, _, userID, _ := setupInternal(t)
	reg := seedOAuthRegistration(t, svc.pool, ScopeUser, userID, "", "http://127.0.0.1:1/mcp")
	flowID := uuid.NewString()
	if _, err := svc.db.CreateMCPOAuthFlow(context.Background(), flowParams(flowID, reg, userID, "verifier", []byte(`{"client_id":"c","token_endpoint":"http://tok","redirect_uri":"http://cb"}`), time.Now().UTC().Add(-time.Minute))); err != nil {
		t.Fatalf("seed expired flow: %v", err)
	}
	if _, err := svc.CompleteOAuth(context.Background(), flowID, "code"); err == nil {
		t.Fatal("expired flow must fail")
	} else if !strings.Contains(err.Error(), "unknown, expired, or already used") {
		t.Fatalf("expired flow error = %v", err)
	}
}

// TestOAuthPerUserBundlesAreIsolated proves a per_user flow writes the bundle
// under the initiating user's user scope only: user A's tools resolve, user B
// has no credential and is skipped by the provider.
func TestOAuthPerUserBundlesAreIsolated(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userA, agentID := setupInternal(t)
	userB := newUserForOAuthTest(t, svc.pool)
	as := newFakeAS(t)
	mcpSrv := fakeMCPServer(t, as.ts.URL)
	as.resource = mcpSrv.URL
	reg := seedOAuthRegistration(t, svc.pool, ScopeSystemAgent, "", agentID, mcpSrv.URL)
	reg, err := svc.GetMCPServerForOwner(context.Background(), reg.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Set credential_mode per_user directly (Create validates the coupling, and
	// per_user is legal on oauth; the seeded row predates the flag).
	if _, err := svc.pool.Exec(context.Background(),
		`UPDATE mcp_server SET credential_mode = 'per_user' WHERE id = $1`, reg.ID); err != nil {
		t.Fatalf("set per_user: %v", err)
	}
	reg.CredentialMode = CredentialModePerUser
	svc.SetConnectForTesting(func(context.Context, Registration, CredentialOwner) (RemoteClient, error) {
		return &fakeMCPClient{tools: []*mcpsdk.Tool{{Name: "list", InputSchema: map[string]any{"type": "object"}}}}, nil
	})

	flowID := startFlow(t, svc, as, reg, userA)
	if _, err := svc.CompleteOAuth(context.Background(), flowID, "auth-code"); err != nil {
		t.Fatalf("CompleteOAuth (A): %v", err)
	}

	if !svc.HasUserCredential(context.Background(), reg, userA) {
		t.Fatal("user A must have a credential after connecting")
	}
	if svc.HasUserCredential(context.Background(), reg, userB) {
		t.Fatal("user B must not inherit user A's per_user bundle")
	}
	// The provider serves tools for A and skips the server for B.
	provider := NewToolProvider(svc)
	if tools := provider.ToolsForContext(context.Background(), userA, agentID); len(tools) != 1 {
		t.Fatalf("A tools = %d, want 1", len(tools))
	}
	if tools := provider.ToolsForContext(context.Background(), userB, agentID); len(tools) != 0 {
		t.Fatalf("B tools = %d, want 0 (no bundle)", len(tools))
	}
}
