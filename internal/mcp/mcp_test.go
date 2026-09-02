package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// fakeDB is an in-memory mcp.DB for unit tests.
type fakeDB struct {
	created       []sqlc.CreateMCPServerParams
	gets          int
	rows          map[string]sqlc.McpServer // id -> row
	forCtx        []sqlc.McpServer          // canned ResolveForContext result
	byScope       []sqlc.McpServer
	updated       []sqlc.UpdateMCPServerByScopeParams
	probeResults  []sqlc.UpdateMCPServerProbeResultParams
	statusUpdates []sqlc.UpdateMCPServerStatusParams
	deleted       []string
	createFn      func(sqlc.CreateMCPServerParams) (sqlc.McpServer, error)
}

func newFakeDB() *fakeDB { return &fakeDB{rows: map[string]sqlc.McpServer{}} }

func (d *fakeDB) CreateMCPServer(_ context.Context, arg sqlc.CreateMCPServerParams) (sqlc.McpServer, error) {
	d.created = append(d.created, arg)
	if d.createFn != nil {
		return d.createFn(arg)
	}
	row := sqlc.McpServer{
		ID: arg.ID, Scope: arg.Scope, UserID: arg.UserID, AgentID: arg.AgentID,
		Name: arg.Name, Url: arg.Url, Transport: arg.Transport, AuthType: arg.AuthType,
		CredentialRef: arg.CredentialRef, Enabled: arg.Enabled, Metadata: arg.Metadata,
	}
	d.rows[arg.ID] = row
	return row, nil
}

func (d *fakeDB) GetMCPServerByID(_ context.Context, id string) (sqlc.McpServer, error) {
	d.gets++
	return d.rows[id], nil
}

func (d *fakeDB) ListMCPServersByScope(_ context.Context, _ sqlc.ListMCPServersByScopeParams) ([]sqlc.McpServer, error) {
	return d.byScope, nil
}

func (d *fakeDB) ListMCPServersForAgentContext(_ context.Context, _ sqlc.ListMCPServersForAgentContextParams) ([]sqlc.McpServer, error) {
	return d.forCtx, nil
}

func (d *fakeDB) UpdateMCPServerByScope(_ context.Context, arg sqlc.UpdateMCPServerByScopeParams) (sqlc.McpServer, error) {
	d.updated = append(d.updated, arg)
	row := d.rows[arg.ID]
	row.Scope = arg.NewScope
	row.UserID = arg.NewUserID
	row.AgentID = arg.NewAgentID
	row.Name = arg.Name
	row.Url = arg.Url
	row.Transport = arg.Transport
	row.AuthType = arg.AuthType
	row.CredentialRef = arg.CredentialRef
	row.Enabled = arg.Enabled
	d.rows[arg.ID] = row
	return row, nil
}

func (d *fakeDB) UpdateMCPServerByScopeIfVersion(ctx context.Context, arg sqlc.UpdateMCPServerByScopeIfVersionParams) (sqlc.McpServer, error) {
	row, ok := d.rows[arg.ID]
	if !ok || !row.UpdatedAt.Equal(arg.ExpectedUpdatedAt) {
		return sqlc.McpServer{}, pgx.ErrNoRows
	}
	return d.UpdateMCPServerByScope(ctx, sqlc.UpdateMCPServerByScopeParams{
		NewScope: arg.NewScope, NewUserID: arg.NewUserID, NewAgentID: arg.NewAgentID,
		Name: arg.Name, Url: arg.Url, Transport: arg.Transport, AuthType: arg.AuthType,
		CredentialRef: arg.CredentialRef, Enabled: arg.Enabled, ID: arg.ID, Scope: arg.Scope,
		UserID: arg.UserID, AgentID: arg.AgentID,
	})
}

func (d *fakeDB) UpdateMCPServerProbeResult(_ context.Context, arg sqlc.UpdateMCPServerProbeResultParams) (sqlc.McpServer, error) {
	d.probeResults = append(d.probeResults, arg)
	row := d.rows[arg.ID]
	row.Status, row.StatusError, row.ProbedAt, row.Tools = arg.Status, arg.StatusError, arg.ProbedAt, arg.Tools
	d.rows[arg.ID] = row
	return row, nil
}

func (d *fakeDB) UpdateMCPServerStatus(_ context.Context, arg sqlc.UpdateMCPServerStatusParams) error {
	d.statusUpdates = append(d.statusUpdates, arg)
	row := d.rows[arg.ID]
	row.Status, row.StatusError = arg.Status, arg.StatusError
	d.rows[arg.ID] = row
	return nil
}

func (d *fakeDB) DeleteMCPServerByScope(_ context.Context, arg sqlc.DeleteMCPServerByScopeParams) error {
	d.deleted = append(d.deleted, arg.ID)
	return nil
}

func (d *fakeDB) DeleteMCPServerByScopeIfVersion(_ context.Context, arg sqlc.DeleteMCPServerByScopeIfVersionParams) (int64, error) {
	row, ok := d.rows[arg.ID]
	if !ok || !row.UpdatedAt.Equal(arg.ExpectedUpdatedAt) {
		return 0, nil
	}
	d.deleted = append(d.deleted, arg.ID)
	return 1, nil
}

func TestToolMutationSchemasRequireNonEmptyExpectedVersion(t *testing.T) {
	for _, action := range []string{"update", "delete"} {
		var schema map[string]any
		for _, spec := range SettingsMcpActionTools() {
			if spec.Action == action {
				if err := json.Unmarshal([]byte(spec.InputSchemaJSON), &schema); err != nil {
					t.Fatalf("decode %s schema: %v", action, err)
				}
				break
			}
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema has no properties: %#v", action, schema)
		}
		expected, ok := properties["expected_version"].(map[string]any)
		if !ok || expected["minLength"] != float64(1) {
			t.Fatalf("%s expected_version schema = %#v, want minLength 1", action, expected)
		}
	}
}

func TestToolMutationDispatchRejectsBlankExpectedVersionBeforeService(t *testing.T) {
	db := newFakeDB()
	db.rows["server"] = sqlc.McpServer{ID: "server", Scope: ScopeUser, UserID: pgnull.Text("user"), Name: "before", Url: "https://mcp.example.test", Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: time.Now().UTC()}
	svc := NewService(db, nil)
	authority, err := authz.NewUserAuthority(authz.UserID("user"), false)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	access, err := NewAccess(svc, nil, nil).Begin(authority)
	if err != nil {
		t.Fatalf("begin access: %v", err)
	}
	handler := managementHandler{access: access}
	for _, tc := range []struct {
		action string
		args   map[string]any
	}{
		{action: "update", args: map[string]any{"id": "server", "expected_version": "", "enabled": false}},
		{action: "delete", args: map[string]any{"id": "server", "expected_version": ""}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			if _, err := SettingsMcpDispatch(t.Context(), handler, tc.action, tc.args); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("dispatch = %v, want version conflict", err)
			}
		})
	}
	if db.gets != 0 || len(db.updated) != 0 || len(db.deleted) != 0 {
		t.Fatalf("blank version reached service: gets=%d updates=%d deletes=%d", db.gets, len(db.updated), len(db.deleted))
	}
}

func TestUpdateIfVersionRejectsChangedRegistration(t *testing.T) {
	db := newFakeDB()
	updatedAt := time.Now().UTC().Add(-time.Minute)
	db.rows["server"] = sqlc.McpServer{ID: "server", Scope: ScopeUser, UserID: pgnull.Text("user"), Name: "before", Url: "https://mcp.example.test", Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: updatedAt}
	svc := NewService(db, nil)
	observed := registrationFromRow(db.rows["server"])

	// Simulate a committed write between the caller's get and its mutation.
	row := db.rows["server"]
	row.UpdatedAt = row.UpdatedAt.Add(time.Second)
	db.rows["server"] = row
	name := "after"
	_, err := svc.UpdateIfVersion(t.Context(), UpdateInput{ID: "server", Scope: ScopeUser, UserID: "user", Name: &name}, observed.Version())
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("UpdateIfVersion error = %v, want version conflict", err)
	}
	if err := svc.DeleteIfVersion(t.Context(), "server", ScopeUser, "user", "", observed.Version()); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("DeleteIfVersion error = %v, want version conflict", err)
	}
	if len(db.deleted) != 0 {
		t.Fatalf("stale delete mutated registration: %v", db.deleted)
	}
}

func TestToolVersionPathsRejectBlankBeforeMutation(t *testing.T) {
	db := newFakeDB()
	db.rows["server"] = sqlc.McpServer{ID: "server", Scope: ScopeUser, UserID: pgnull.Text("user"), Name: "before", Url: "https://mcp.example.test", Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: time.Now().UTC()}
	svc := NewService(db, nil)
	name := "after"
	if _, err := svc.UpdateIfVersion(t.Context(), UpdateInput{ID: "server", Scope: ScopeUser, UserID: "user", Name: &name}, ""); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("blank UpdateIfVersion = %v, want version conflict", err)
	}
	if err := svc.DeleteIfVersion(t.Context(), "server", ScopeUser, "user", "", ""); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("blank DeleteIfVersion = %v, want version conflict", err)
	}
	if db.gets != 0 || len(db.updated) != 0 || len(db.deleted) != 0 {
		t.Fatalf("blank version mutated registration: gets=%d updates=%d deletes=%d", db.gets, len(db.updated), len(db.deleted))
	}
}

// fakeVault records the plaintext handed to it, keyed by name, and returns it
// back on GetScoped. It stands in for the age-encrypting vault in unit tests;
// the integration test proves the ciphertext is unreadable at rest.
type fakeVault struct {
	stored map[string]string
}

func newFakeVault() *fakeVault { return &fakeVault{stored: map[string]string{}} }

func vaultKey(scope, userID, agentID, name string) string {
	return scope + "|" + userID + "|" + agentID + "|" + name
}

func (v *fakeVault) SetScoped(_ context.Context, scope, userID, agentID, name, plaintext string) error {
	v.stored[vaultKey(scope, userID, agentID, name)] = plaintext
	return nil
}

func (v *fakeVault) SetSystemScoped(_ context.Context, scope, agentID, name, plaintext string) error {
	v.stored[vaultKey(scope, "", agentID, name)] = plaintext
	return nil
}

func (v *fakeVault) GetScoped(_ context.Context, scope, userID, agentID, name string) (string, error) {
	return v.stored[vaultKey(scope, userID, agentID, name)], nil
}

func (v *fakeVault) DeleteScoped(_ context.Context, scope, userID, agentID, name string) error {
	delete(v.stored, vaultKey(scope, userID, agentID, name))
	return nil
}

func (v *fakeVault) DeleteSystemScoped(_ context.Context, scope, agentID, name string) error {
	delete(v.stored, vaultKey(scope, "", agentID, name))
	return nil
}

func TestValidTransportRejectsStdio(t *testing.T) {
	if ValidTransport("stdio") {
		t.Fatal("stdio must be rejected")
	}
	if !ValidTransport(TransportStreamableHTTP) || !ValidTransport(TransportSSE) {
		t.Fatal("streamable_http and sse must be accepted")
	}
	if ValidTransport("") || ValidTransport("websocket") {
		t.Fatal("only HTTP-based transports are valid")
	}
}

func TestValidateEndpointURLRejectsUnsafeTargets(t *testing.T) {
	bad := []string{
		"ftp://example.com/mcp",
		"https://user:pass@example.com/mcp",
		"http://localhost/mcp",
		"http://127.0.0.1/mcp",
		"http://10.0.0.1/mcp",
		"http://172.16.0.1/mcp",
		"http://192.168.1.1/mcp",
		"http://169.254.169.254/latest/meta-data",
		"http://100.64.0.1/mcp",
		"http://100.127.255.254/mcp",
		"http://[::1]/mcp",
		"http://[64:ff9b::c000:201]/mcp",
		"http://[::ffff:100.64.0.1]/mcp",
		"https://example.com/mcp?token=secret",
		"https://example.com/mcp#secret",
	}
	for _, raw := range bad {
		if err := validateEndpointURL(raw); err == nil {
			t.Fatalf("validateEndpointURL(%q) succeeded, want rejection", raw)
		}
	}
	for _, raw := range []string{
		"https://example.com/mcp",
		"http://100.63.255.255/mcp",
		"http://100.128.0.0/mcp",
		"https://[64:ff9b:1::1]/mcp",
	} {
		if err := validateEndpointURL(raw); err != nil {
			t.Fatalf("public endpoint %q rejected: %v", raw, err)
		}
	}
}

type recordingRoundTripper struct{ request *http.Request }

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.request = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

func TestAuthRoundTripperClonesRequestBeforeAddingBearer(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://example.com/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("X-Caller", "unchanged")
	base := &recordingRoundTripper{}
	response, err := (&authRoundTripper{base: base, bearer: "secret"}).RoundTrip(request)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
	if base.request == request {
		t.Fatal("outbound request must be cloned before adding authorization")
	}
	if got := base.request.Header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("outbound authorization = %q, want bearer token", got)
	}
	if got := request.Header.Get("Authorization"); got != "" {
		t.Fatalf("original request authorization = %q, want unchanged", got)
	}
	if got := request.Header.Get("X-Caller"); got != "unchanged" {
		t.Fatalf("original request header = %q, want unchanged", got)
	}
}

func TestAuthRoundTripperOmitsEmptyBearer(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://example.com/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	base := &recordingRoundTripper{}
	response, err := (&authRoundTripper{base: base}).RoundTrip(request)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
	if got := base.request.Header.Get("Authorization"); got != "" {
		t.Fatalf("outbound authorization = %q, want none", got)
	}
}

func TestSafeHTTPClientRedirectPolicy(t *testing.T) {
	client := safeHTTPClient("secret")
	first, err := http.NewRequest(http.MethodGet, "https://example.com/mcp", nil)
	if err != nil {
		t.Fatalf("new first request: %v", err)
	}
	for _, tc := range []struct {
		name    string
		target  string
		wantErr bool
	}{
		{name: "same origin", target: "https://EXAMPLE.COM/next"},
		{name: "other public origin", target: "https://other.example.com/next", wantErr: true},
		{name: "unsafe private target", target: "http://127.0.0.1/next", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := http.NewRequest(http.MethodGet, tc.target, nil)
			if err != nil {
				t.Fatalf("new redirect request: %v", err)
			}
			err = client.CheckRedirect(target, []*http.Request{first})
			if (err != nil) != tc.wantErr {
				t.Fatalf("CheckRedirect(%q) error = %v, wantErr = %v", tc.target, err, tc.wantErr)
			}
		})
	}
}

func TestBuildTransportRejectsStdio(t *testing.T) {
	if _, err := buildTransport(Registration{Transport: "stdio", URL: "http://x"}, ""); err == nil {
		t.Fatal("buildTransport must reject stdio")
	}
	tr, err := buildTransport(Registration{Transport: TransportStreamableHTTP, URL: "http://x"}, "tok")
	if err != nil {
		t.Fatalf("streamable_http: %v", err)
	}
	if _, ok := tr.(*mcpsdk.StreamableClientTransport); !ok {
		t.Fatalf("streamable_http: got %T", tr)
	}
	tr, err = buildTransport(Registration{Transport: TransportSSE, URL: "http://x"}, "")
	if err != nil {
		t.Fatalf("sse: %v", err)
	}
	if _, ok := tr.(*mcpsdk.SSEClientTransport); !ok {
		t.Fatalf("sse: got %T", tr)
	}
}

func TestCreateRejectsStdioTransport(t *testing.T) {
	db := newFakeDB()
	svc := NewService(db, newFakeVault())
	_, err := svc.Create(context.Background(), CreateInput{
		Scope: ScopeUser, UserID: "u1", Name: "gh", URL: "http://x", Transport: "stdio",
	})
	if err == nil {
		t.Fatal("Create must reject stdio transport")
	}
	if len(db.created) != 0 {
		t.Fatalf("no row should be inserted for an invalid transport; got %d", len(db.created))
	}
}

func TestCreateBearerStoresTokenInVaultNotRow(t *testing.T) {
	db := newFakeDB()
	vlt := newFakeVault()
	svc := NewService(db, vlt)
	const token = "secret-bearer-123"

	reg, err := svc.Create(context.Background(), CreateInput{
		Scope: ScopeUser, UserID: "u1", Name: "gh", URL: "https://mcp.example.com",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeBearer, Token: token,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The row must reference the credential, never carry the secret itself.
	if len(db.created) != 1 {
		t.Fatalf("want 1 insert, got %d", len(db.created))
	}
	arg := db.created[0]
	if arg.CredentialRef == "" {
		t.Fatal("credential_ref must be set for bearer auth")
	}
	if arg.CredentialRef == token {
		t.Fatal("credential_ref must not be the token itself")
	}

	// The token must have been handed to the vault under the credential name.
	got, ok := vlt.stored[vaultKey(ScopeUser, "u1", "", reg.CredentialRef)]
	if !ok {
		t.Fatalf("token was not stored in the vault under %q", reg.CredentialRef)
	}
	if got != token {
		t.Fatalf("vault stored %q, want the raw token %q", got, token)
	}

	// And the service can read it back for connecting.
	back, err := svc.BearerToken(context.Background(), reg)
	if err != nil {
		t.Fatalf("BearerToken: %v", err)
	}
	if back != token {
		t.Fatalf("BearerToken = %q, want %q", back, token)
	}

	// The credential name must be a valid vault entry name.
	if err := vault.ValidateName(reg.CredentialRef); err != nil {
		t.Fatalf("credential name %q is not a valid vault name: %v", reg.CredentialRef, err)
	}
}

func strPtr(s string) *string { return &s }

func TestUpdateBearerRejectsScopeMoveWithoutReplacement(t *testing.T) {
	db := newFakeDB()
	vlt := newFakeVault()
	svc := NewService(db, vlt)
	reg, err := svc.Create(context.Background(), CreateInput{
		Scope: ScopeUser, UserID: "u1", Name: "gh", URL: "https://old.example.com",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeBearer, Token: "secret",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = svc.Update(context.Background(), UpdateInput{
		ID: reg.ID, Scope: ScopeUser, UserID: "u1",
		NewScope: strPtr(ScopeUserAgent), NewUserID: "u1", NewAgentID: "a1",
		URL: strPtr("https://new.example.com"),
	})
	if err == nil {
		t.Fatal("moving bearer registration without replacement credentials must fail")
	}
	if _, ok := vlt.stored[vaultKey(ScopeUserAgent, "u1", "a1", reg.CredentialRef)]; ok {
		t.Fatal("scope move must not copy the existing bearer")
	}
	if got := vlt.stored[vaultKey(ScopeUser, "u1", "", reg.CredentialRef)]; got != "secret" {
		t.Fatalf("original token = %q, want unchanged", got)
	}
}

func TestUpdateAuthNonePurgesToken(t *testing.T) {
	db := newFakeDB()
	vlt := newFakeVault()
	svc := NewService(db, vlt)
	reg, err := svc.Create(context.Background(), CreateInput{
		Scope: ScopeUser, UserID: "u1", Name: "gh", URL: "https://mcp.example.com",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeBearer, Token: "secret",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := svc.Update(context.Background(), UpdateInput{
		ID: reg.ID, Scope: ScopeUser, UserID: "u1", AuthType: strPtr(AuthTypeNone),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.AuthType != AuthTypeNone || updated.CredentialRef != "" {
		t.Fatalf("updated auth = %q cred = %q", updated.AuthType, updated.CredentialRef)
	}
	if _, ok := vlt.stored[vaultKey(ScopeUser, "u1", "", reg.CredentialRef)]; ok {
		t.Fatal("token should be deleted when auth switches to none")
	}
}

func TestCreateBearerRequiresTokenAndVault(t *testing.T) {
	// Missing token.
	if _, err := NewService(newFakeDB(), newFakeVault()).Create(context.Background(), CreateInput{
		Scope: ScopeUser, UserID: "u1", Name: "gh", URL: "http://x", AuthType: AuthTypeBearer,
	}); err == nil {
		t.Fatal("bearer without token must fail")
	}
	// No vault configured.
	if _, err := NewService(newFakeDB(), nil).Create(context.Background(), CreateInput{
		Scope: ScopeUser, UserID: "u1", Name: "gh", URL: "http://x", AuthType: AuthTypeBearer, Token: "t",
	}); err == nil {
		t.Fatal("bearer without vault must fail")
	}
}

func TestCreateScopeOwnerValidation(t *testing.T) {
	svc := NewService(newFakeDB(), newFakeVault())
	cases := []struct {
		name    string
		in      CreateInput
		wantErr bool
	}{
		{"user ok", CreateInput{Scope: ScopeUser, UserID: "u1", Name: "n", URL: "http://x"}, false},
		{"user missing user_id", CreateInput{Scope: ScopeUser, Name: "n", URL: "http://x"}, true},
		{"user with agent", CreateInput{Scope: ScopeUser, UserID: "u1", AgentID: "a1", Name: "n", URL: "http://x"}, true},
		{"user_agent ok", CreateInput{Scope: ScopeUserAgent, UserID: "u1", AgentID: "a1", Name: "n", URL: "http://x"}, false},
		{"user_agent missing agent", CreateInput{Scope: ScopeUserAgent, UserID: "u1", Name: "n", URL: "http://x"}, true},
		{"system ok", CreateInput{Scope: ScopeSystem, Name: "n", URL: "http://x"}, false},
		{"system with user", CreateInput{Scope: ScopeSystem, UserID: "u1", Name: "n", URL: "http://x"}, true},
		{"system_agent ok", CreateInput{Scope: ScopeSystemAgent, AgentID: "a1", Name: "n", URL: "http://x"}, false},
		{"bad scope", CreateInput{Scope: "nope", Name: "n", URL: "http://x"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), tc.in)
			if tc.wantErr != (err != nil) {
				t.Fatalf("Create(%+v) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			}
		})
	}
}

func TestResolveForContextDedupPrecedence(t *testing.T) {
	db := newFakeDB()
	// The SQL orders most-specific-first; the service keeps the first per name.
	db.forCtx = []sqlc.McpServer{
		{ID: "1", Scope: ScopeUserAgent, Name: "gh", UserID: pgnull.Text("u1"), AgentID: pgnull.Text("a1")},
		{ID: "2", Scope: ScopeUser, Name: "gh", UserID: pgnull.Text("u1")},
		{ID: "3", Scope: ScopeSystemAgent, Name: "gh", AgentID: pgnull.Text("a1")},
		{ID: "4", Scope: ScopeSystem, Name: "gh"},
		{ID: "5", Scope: ScopeSystem, Name: "other"},
	}
	svc := NewService(db, newFakeVault())
	regs, err := svc.ResolveForContext(context.Background(), "u1", "a1")
	if err != nil {
		t.Fatalf("ResolveForContext: %v", err)
	}
	if len(regs) != 2 {
		t.Fatalf("want 2 deduped servers, got %d", len(regs))
	}
	byName := map[string]Registration{}
	for _, r := range regs {
		byName[r.Name] = r
	}
	if byName["gh"].ID != "1" {
		t.Fatalf("gh precedence: kept id %q, want user_agent id 1", byName["gh"].ID)
	}
	if byName["other"].ID != "5" {
		t.Fatalf("other: kept id %q, want 5", byName["other"].ID)
	}
}

type fakeMCPClient struct {
	tools      []*mcpsdk.Tool
	listErr    error
	listFn     func(context.Context) ([]*mcpsdk.Tool, error)
	result     *mcpsdk.CallToolResult
	callErr    error
	callFn     func(context.Context, string, map[string]any) (*mcpsdk.CallToolResult, error)
	closeOnce  sync.Once
	closeCalls atomic.Int32
	closeCount atomic.Int32
}

func (c *fakeMCPClient) ListTools(ctx context.Context) ([]*mcpsdk.Tool, error) {
	if c.listFn != nil {
		return c.listFn(ctx)
	}
	return c.tools, c.listErr
}

func (c *fakeMCPClient) CallTool(ctx context.Context, name string, args map[string]any) (*mcpsdk.CallToolResult, error) {
	if c.callFn != nil {
		return c.callFn(ctx, name, args)
	}
	return c.result, c.callErr
}

func (c *fakeMCPClient) Close() error {
	c.closeCalls.Add(1)
	c.closeOnce.Do(func() { c.closeCount.Add(1) })
	return nil
}

// seedRow inserts a registration row with probe state for provider tests.
func seedRow(d *fakeDB, id, name, status string, probedAt time.Time, tools []CatalogTool) {
	toolsJSON, err := json.Marshal(tools)
	if err != nil {
		toolsJSON = []byte("[]")
	}
	d.rows[id] = sqlc.McpServer{
		ID: id, Scope: ScopeUser, UserID: pgnull.Text("u1"), Name: name, Url: "https://mcp.example.com",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true,
		Status: status, ProbedAt: pgtype.Timestamptz{Time: probedAt, Valid: !probedAt.IsZero()}, Tools: toolsJSON,
		UpdatedAt: probedAt,
	}
	d.forCtx = append(d.forCtx, d.rows[id])
}

func catalogRow(d *fakeDB, id, name string, toolNames ...string) {
	now := time.Now().UTC()
	tools := make([]CatalogTool, 0, len(toolNames))
	for _, n := range toolNames {
		tools = append(tools, CatalogTool{Name: n, Description: "desc", InputSchema: map[string]any{"type": "object"}})
	}
	seedRow(d, id, name, StatusOK, now, tools)
}

func TestToolProviderUsesPersistedCatalogWithoutConnecting(t *testing.T) {
	db := newFakeDB()
	catalogRow(db, "srv1", "gh", "create_issue")
	svc := NewService(db, newFakeVault())
	connects := 0
	svc.connect = func(context.Context, Registration, string) (RemoteClient, error) {
		connects++
		return &fakeMCPClient{}, nil
	}
	provider := NewToolProvider(svc)

	tools := provider.ToolsForContext(context.Background(), "u1", "a1")
	if connects != 0 {
		t.Fatalf("connects = %d, want 0 with a fresh persisted catalog", connects)
	}
	if len(tools) != 1 || tools[0].Definition().Name != "mcp__gh__create_issue" {
		t.Fatalf("tools = %#v, want the cataloged tool", tools)
	}
}

func TestToolProviderStaleCatalogTriggersColdDiscovery(t *testing.T) {
	db := newFakeDB()
	seedRow(db, "srv1", "gh", StatusOK, time.Now().UTC().Add(-25*time.Hour), []CatalogTool{{Name: "old_tool", InputSchema: map[string]any{"type": "object"}}})
	svc := NewService(db, newFakeVault())
	svc.probeTimeout = time.Second
	connects := 0
	svc.connect = func(context.Context, Registration, string) (RemoteClient, error) {
		connects++
		return &fakeMCPClient{tools: []*mcpsdk.Tool{
			{Name: "new_tool", Description: "fresh", InputSchema: map[string]any{"type": "object"}},
		}}, nil
	}
	provider := NewToolProvider(svc)

	tools := provider.ToolsForContext(context.Background(), "u1", "a1")
	if connects != 1 {
		t.Fatalf("connects = %d, want 1 cold discovery", connects)
	}
	if len(tools) != 1 || tools[0].Definition().Name != "mcp__gh__new_tool" {
		t.Fatalf("tools = %#v, want tools from the refreshed catalog", tools)
	}
	if len(db.probeResults) != 1 || db.probeResults[0].Status != StatusOK {
		t.Fatalf("cold discovery result not persisted: %+v", db.probeResults)
	}
}

func TestToolProviderNeedsAuthSkippedWithoutConnecting(t *testing.T) {
	db := newFakeDB()
	seedRow(db, "srv1", "gh", StatusNeedsAuth, time.Now().UTC(), nil)
	svc := NewService(db, newFakeVault())
	svc.connect = func(context.Context, Registration, string) (RemoteClient, error) {
		t.Fatal("needs_auth server must be skipped without connecting")
		return nil, nil
	}
	if tools := NewToolProvider(svc).ToolsForContext(context.Background(), "u1", "a1"); len(tools) != 0 {
		t.Fatalf("tools = %d, want none", len(tools))
	}
}

func TestToolProviderDiscoversServersConcurrently(t *testing.T) {
	db := newFakeDB()
	for i, id := range []string{"srv1", "srv2", "srv3"} {
		seedRow(db, id, id, StatusUnknown, time.Time{}, nil)
		_ = i
	}
	svc := NewService(db, newFakeVault())
	svc.probeTimeout = time.Second
	var current atomic.Int32
	var maxSeen atomic.Int32
	svc.connect = func(ctx context.Context, reg Registration, _ string) (RemoteClient, error) {
		cur := current.Add(1)
		for {
			max := maxSeen.Load()
			if cur <= max || maxSeen.CompareAndSwap(max, cur) {
				break
			}
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
		}
		current.Add(-1)
		return &fakeMCPClient{tools: []*mcpsdk.Tool{{Name: reg.Name, InputSchema: map[string]any{"type": "object"}}}}, nil
	}
	provider := NewToolProvider(svc)
	provider.concurrency = 3

	tools := provider.ToolsForContext(context.Background(), "u1", "a1")
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}
	if maxSeen.Load() < 2 {
		t.Fatalf("MCP discovery ran serially; max concurrency = %d", maxSeen.Load())
	}
}

func TestToolProviderFailedDiscoveryPersistsErrorAndSkips(t *testing.T) {
	db := newFakeDB()
	seedRow(db, "srv1", "broken", StatusUnknown, time.Time{}, nil)
	svc := NewService(db, newFakeVault())
	svc.probeTimeout = time.Second
	client := &fakeMCPClient{listErr: errors.New("list failed")}
	svc.connect = func(context.Context, Registration, string) (RemoteClient, error) { return client, nil }

	if tools := NewToolProvider(svc).ToolsForContext(context.Background(), "u1", "a1"); len(tools) != 0 {
		t.Fatalf("tools = %d, want none after discovery failure", len(tools))
	}
	if len(db.probeResults) != 1 || db.probeResults[0].Status != StatusError {
		t.Fatalf("discovery failure not persisted: %+v", db.probeResults)
	}
	if got := client.closeCount.Load(); got != 1 {
		t.Fatalf("failed discovery close count = %d, want 1 (probe closes its client)", got)
	}
}

func TestToolProviderSkipsCollidingToolNames(t *testing.T) {
	db := newFakeDB()
	catalogRow(db, "srv1", "git-hub", "foo-bar")
	catalogRow(db, "srv2", "git_hub", "foo-bar")
	svc := NewService(db, newFakeVault())
	svc.connect = func(context.Context, Registration, string) (RemoteClient, error) {
		t.Fatal("persisted catalogs must not connect")
		return nil, nil
	}

	tools := NewToolProvider(svc).ToolsForContext(context.Background(), "u1", "a1")
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want one duplicate skipped", len(tools))
	}
	if got := tools[0].Definition().Name; got != "mcp__git_hub__foo_bar" {
		t.Fatalf("tool name = %q", got)
	}
}

func TestProbeSuccessPersistsToolsAndStatus(t *testing.T) {
	db := newFakeDB()
	db.rows["srv1"] = sqlc.McpServer{ID: "srv1", Scope: ScopeUser, UserID: pgnull.Text("u1"), Name: "gh", Url: "https://mcp.example.com", Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true}
	svc := NewService(db, newFakeVault())
	svc.probeTimeout = time.Second
	svc.connect = func(context.Context, Registration, string) (RemoteClient, error) {
		return &fakeMCPClient{tools: []*mcpsdk.Tool{
			{Name: "create_issue", Description: "Create issue", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}}, Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true}},
		}}, nil
	}

	reg := registrationFromRow(db.rows["srv1"])
	updated, err := svc.Probe(context.Background(), reg)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if updated.Status != StatusOK || updated.StatusError != "" {
		t.Fatalf("status = %q/%q, want ok with no error", updated.Status, updated.StatusError)
	}
	if updated.ProbedAt.IsZero() {
		t.Fatal("probed_at not set")
	}
	if len(updated.Tools) != 1 || updated.Tools[0].Name != "create_issue" {
		t.Fatalf("tools = %#v, want the remote tool snapshot", updated.Tools)
	}
	if updated.Tools[0].InputSchema["type"] != "object" {
		t.Fatalf("input_schema = %#v", updated.Tools[0].InputSchema)
	}
	if got, ok := updated.Tools[0].Annotations["readOnlyHint"].(bool); !ok || !got {
		t.Fatalf("annotations = %#v, want readOnlyHint true", updated.Tools[0].Annotations)
	}
	if got := registrationFromRow(db.rows["srv1"]).Version(); got != reg.Version() {
		t.Fatal("probe must not change Version()")
	}
}

func TestProbeFailurePersistsRedactedError(t *testing.T) {
	db := newFakeDB()
	db.rows["srv1"] = sqlc.McpServer{ID: "srv1", Scope: ScopeUser, Name: "gh", Url: "https://mcp.example.com", Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true}
	svc := NewService(db, newFakeVault())
	svc.probeTimeout = time.Second
	svc.connect = func(context.Context, Registration, string) (RemoteClient, error) {
		return nil, fmt.Errorf("dial https://user:pass@example.com/mcp?token=secret failed")
	}

	reg := registrationFromRow(db.rows["srv1"])
	updated, err := svc.Probe(context.Background(), reg)
	if err != nil {
		t.Fatalf("Probe must not fail the caller: %v", err)
	}
	if updated.Status != StatusError {
		t.Fatalf("status = %q, want error", updated.Status)
	}
	msg := updated.StatusError
	for _, secret := range []string{"user:pass", "token=secret", "https://mcp.example.com"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("probe error leaked %q: %s", secret, msg)
		}
	}
	if !strings.Contains(msg, "example.com/mcp") {
		t.Fatalf("probe error lost its redacted endpoint: %s", msg)
	}
}

func TestProbeCredentialRejectionSetsNeedsAuth(t *testing.T) {
	db := newFakeDB()
	db.rows["srv1"] = sqlc.McpServer{ID: "srv1", Scope: ScopeUser, Name: "gh", Url: "https://mcp.example.com", Transport: TransportStreamableHTTP, AuthType: AuthTypeBearer, Enabled: true}
	svc := NewService(db, newFakeVault())
	svc.probeTimeout = time.Second
	svc.connect = func(context.Context, Registration, string) (RemoteClient, error) {
		return &fakeMCPClient{listErr: errors.New("tools/list: Unauthorized")}, nil
	}

	updated, err := svc.Probe(context.Background(), registrationFromRow(db.rows["srv1"]))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if updated.Status != StatusNeedsAuth {
		t.Fatalf("status = %q, want needs_auth", updated.Status)
	}
}

func TestExecuteContentConvertsImageBlocks(t *testing.T) {
	proxy := &toolProxy{
		reg:        Registration{Name: "gh"},
		remoteName: "render",
		def:        pkgtools.Definition{Name: "mcp__gh__render"},
		svc:        NewService(newFakeDB(), newFakeVault()),
	}
	svc := proxy.svc
	svc.connect = func(context.Context, Registration, string) (RemoteClient, error) {
		return &fakeMCPClient{result: &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: "here"},
			&mcpsdk.ImageContent{Data: []byte{0x89, 'P', 'N', 'G'}, MIMEType: "image/png"},
			&mcpsdk.AudioContent{Data: []byte("x"), MIMEType: "audio/wav"}, // non-text/image block: JSON-encoded as text
		}}}, nil
	}
	_ = svc

	blocks, err := proxy.ExecuteContent(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(blocks))
	}
	if tc, ok := blocks[0].(ai.TextContent); !ok || tc.Text != "here" {
		t.Fatalf("block 0 = %#v, want text", blocks[0])
	}
	ic, ok := blocks[1].(ai.ImageContent)
	if !ok {
		t.Fatalf("block 1 = %T, want ai.ImageContent", blocks[1])
	}
	if ic.MimeType != "image/png" {
		t.Fatalf("mime = %q", ic.MimeType)
	}
	if got, err := base64.StdEncoding.DecodeString(ic.Data); err != nil || string(got) != "\x89PNG" {
		t.Fatalf("image data = %q, %v", ic.Data, err)
	}
	// Execute keeps the string contract via the same path.
	text, err := proxy.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(text, "here\n[image: image/png]") {
		t.Fatalf("Execute text = %q", text)
	}
}

func TestCallTimeoutReturnsContextErrorWithoutStatusChange(t *testing.T) {
	db := newFakeDB()
	db.rows["srv1"] = sqlc.McpServer{ID: "srv1", Scope: ScopeUser, Name: "gh", Url: "https://mcp.example.com", Enabled: true}
	proxy := &toolProxy{
		reg:        Registration{ID: "srv1", Name: "gh", Metadata: map[string]any{"call_timeout_seconds": float64(1)}},
		remoteName: "slow",
		def:        pkgtools.Definition{Name: "mcp__gh__slow"},
		svc:        NewService(db, newFakeVault()),
	}
	proxy.svc.connect = func(context.Context, Registration, string) (RemoteClient, error) {
		return &fakeMCPClient{callFn: func(ctx context.Context, _ string, _ map[string]any) (*mcpsdk.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}, nil
	}

	start := time.Now()
	_, err := proxy.Execute(context.Background(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error = %v, want context deadline", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("call returned after %v, want ~1s", elapsed)
	}
	if len(db.statusUpdates) != 0 {
		t.Fatalf("timeout must not change server status: %+v", db.statusUpdates)
	}
}

func TestCredentialRejectionMarksNeedsAuth(t *testing.T) {
	db := newFakeDB()
	db.rows["srv1"] = sqlc.McpServer{ID: "srv1", Scope: ScopeUser, Name: "gh", Url: "https://mcp.example.com", Enabled: true}
	proxy := &toolProxy{
		reg:        Registration{ID: "srv1", Name: "gh"},
		remoteName: "list",
		def:        pkgtools.Definition{Name: "mcp__gh__list"},
		svc:        NewService(db, newFakeVault()),
	}
	proxy.svc.connect = func(context.Context, Registration, string) (RemoteClient, error) {
		return &fakeMCPClient{callErr: errors.New("tools/call: Forbidden")}, nil
	}

	_, err := proxy.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "credential rejected; reconnect in the Web UI") {
		t.Fatalf("Execute error = %v, want credential-rejection guidance", err)
	}
	if len(db.statusUpdates) != 1 || db.statusUpdates[0].Status != StatusNeedsAuth {
		t.Fatalf("status updates = %+v, want needs_auth", db.statusUpdates)
	}
}

func TestCallTimeoutDefaultsAndClamps(t *testing.T) {
	for _, tc := range []struct {
		metadata map[string]any
		want     time.Duration
	}{
		{nil, defaultCallTimeout},
		{map[string]any{"call_timeout_seconds": float64(10)}, 10 * time.Second},
		{map[string]any{"call_timeout_seconds": float64(0)}, defaultCallTimeout},
		{map[string]any{"call_timeout_seconds": float64(100000)}, maxCallTimeoutSeconds * time.Second},
		{map[string]any{"call_timeout_seconds": "ten"}, defaultCallTimeout},
	} {
		if got := callTimeout(Registration{Metadata: tc.metadata}); got != tc.want {
			t.Fatalf("callTimeout(%v) = %v, want %v", tc.metadata, got, tc.want)
		}
	}
}

func TestValidateCredentialMode(t *testing.T) {
	if err := validateCredentialMode(""); err != nil {
		t.Fatalf("empty = %v, want default", err)
	}
	if err := validateCredentialMode(CredentialModeShared); err != nil {
		t.Fatalf("shared = %v", err)
	}
	if err := validateCredentialMode(CredentialModePerUser); err == nil {
		t.Fatal("per_user must be rejected until OAuth exists")
	}
	if err := validateCredentialMode("yolo"); err == nil {
		t.Fatal("unknown mode must be rejected")
	}
}

func TestHTTPDeleteIsIdempotentButCASDeleteConflictsWhenAbsent(t *testing.T) {
	svc := NewService(newFakeDB(), newFakeVault())
	if err := svc.Delete(context.Background(), "missing", ScopeUser, "u1", ""); err != nil {
		t.Fatalf("unconditional delete absent registration: %v", err)
	}
	if err := svc.DeleteIfVersion(context.Background(), "missing", ScopeUser, "u1", "", "version"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("CAS delete absent registration = %v, want ErrVersionConflict", err)
	}
}

func TestDeletePurgesCredential(t *testing.T) {
	db := newFakeDB()
	vlt := newFakeVault()
	svc := NewService(db, vlt)
	reg, err := svc.Create(context.Background(), CreateInput{
		Scope: ScopeUser, UserID: "u1", Name: "gh", URL: "http://x",
		AuthType: AuthTypeBearer, Token: "tok",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := vlt.stored[vaultKey(ScopeUser, "u1", "", reg.CredentialRef)]; !ok {
		t.Fatal("precondition: token should be stored")
	}
	if err := svc.Delete(context.Background(), reg.ID, ScopeUser, "u1", ""); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := vlt.stored[vaultKey(ScopeUser, "u1", "", reg.CredentialRef)]; ok {
		t.Fatal("Delete must purge the vault credential")
	}
	if len(db.deleted) != 1 || db.deleted[0] != reg.ID {
		t.Fatalf("row not deleted: %v", db.deleted)
	}
}

func TestNamespacedToolName(t *testing.T) {
	if got := NamespacedToolName("git hub", "create-issue"); got != "mcp__git_hub__create_issue" {
		t.Fatalf("NamespacedToolName = %q", got)
	}
	if got := NamespacedToolName("!!!", "///"); got != "mcp__server__tool" {
		t.Fatalf("NamespacedToolName fallback = %q", got)
	}
}
