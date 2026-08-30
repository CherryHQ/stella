package mcp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// fakeDB is an in-memory mcp.DB for unit tests.
type fakeDB struct {
	created  []sqlc.CreateMCPServerParams
	rows     map[string]sqlc.McpServer // id -> row
	forCtx   []sqlc.McpServer          // canned ResolveForContext result
	byScope  []sqlc.McpServer
	updated  []sqlc.UpdateMCPServerByScopeParams
	deleted  []string
	createFn func(sqlc.CreateMCPServerParams) (sqlc.McpServer, error)
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
		"http://[::1]/mcp",
		"https://example.com/mcp?token=secret",
		"https://example.com/mcp#secret",
	}
	for _, raw := range bad {
		if err := validateEndpointURL(raw); err == nil {
			t.Fatalf("validateEndpointURL(%q) succeeded, want rejection", raw)
		}
	}
	if err := validateEndpointURL("https://example.com/mcp"); err != nil {
		t.Fatalf("public https endpoint rejected: %v", err)
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

func (c *fakeMCPClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "ok", nil
}

func (c *fakeMCPClient) Close() error {
	c.closeCalls.Add(1)
	c.closeOnce.Do(func() { c.closeCount.Add(1) })
	return nil
}

func TestToolProviderCachesToolList(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	db := newFakeDB()
	db.forCtx = []sqlc.McpServer{{
		ID: "srv1", Scope: ScopeUser, UserID: pgnull.Text("u1"), Name: "gh", Url: "https://mcp.example.com",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: now,
	}}
	provider := NewToolProvider(NewService(db, newFakeVault()))
	provider.now = func() time.Time { return now }
	provider.ttl = time.Minute

	connects := 0
	provider.connect = func(context.Context, Registration, string) (mcpClient, error) {
		connects++
		return &fakeMCPClient{tools: []*mcpsdk.Tool{{Name: "create_issue", Description: "Create issue", InputSchema: map[string]any{"type": "object"}}}}, nil
	}

	first := provider.ToolsForContext(context.Background(), "u1", "a1")
	second := provider.ToolsForContext(context.Background(), "u1", "a1")

	if connects != 1 {
		t.Fatalf("connects = %d, want 1 tools/list connection", connects)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("tool counts = %d/%d, want 1/1", len(first), len(second))
	}
	if got := second[0].Definition().Name; got != "mcp__gh__create_issue" {
		t.Fatalf("cached tool name = %q", got)
	}
}

func TestToolProviderDiscoversServersConcurrently(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	db := newFakeDB()
	for i, id := range []string{"srv1", "srv2", "srv3"} {
		db.forCtx = append(db.forCtx, sqlc.McpServer{
			ID: id, Scope: ScopeUser, UserID: pgnull.Text("u1"), Name: id, Url: "https://mcp.example.com",
			Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: now.Add(time.Duration(i)),
		})
	}
	provider := NewToolProvider(NewService(db, newFakeVault()))
	provider.concurrency = 3
	var current atomic.Int32
	var maxSeen atomic.Int32
	provider.connect = func(ctx context.Context, reg Registration, _ string) (mcpClient, error) {
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

	tools := provider.ToolsForContext(context.Background(), "u1", "a1")
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}
	if maxSeen.Load() < 2 {
		t.Fatalf("MCP discovery ran serially; max concurrency = %d", maxSeen.Load())
	}
}

func TestToolProviderDiscoverySharesDeadlineAndKeepsHealthyTools(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	db := newFakeDB()
	for _, name := range []string{"connect-blocked", "list-blocked", "healthy"} {
		db.forCtx = append(db.forCtx, sqlc.McpServer{
			ID: name, Scope: ScopeUser, UserID: pgnull.Text("u1"), Name: name, Url: "https://mcp.example.com",
			Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: now,
		})
	}
	provider := NewToolProvider(NewService(db, newFakeVault()))
	provider.timeout = 100 * time.Millisecond
	provider.concurrency = 3

	var deadlineCalls atomic.Int32
	var cancelled atomic.Int32
	checkDeadline := func(ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > provider.timeout {
			return
		}
		deadlineCalls.Add(1)
	}
	provider.connect = func(ctx context.Context, reg Registration, _ string) (mcpClient, error) {
		checkDeadline(ctx)
		switch reg.Name {
		case "connect-blocked":
			<-ctx.Done()
			cancelled.Add(1)
			return nil, ctx.Err()
		case "list-blocked":
			return &fakeMCPClient{listFn: func(ctx context.Context) ([]*mcpsdk.Tool, error) {
				checkDeadline(ctx)
				<-ctx.Done()
				cancelled.Add(1)
				return nil, ctx.Err()
			}}, nil
		case "healthy":
			return &fakeMCPClient{listFn: func(ctx context.Context) ([]*mcpsdk.Tool, error) {
				checkDeadline(ctx)
				return []*mcpsdk.Tool{{Name: "available", InputSchema: map[string]any{"type": "object"}}}, nil
			}}, nil
		default:
			return nil, errors.New("unexpected server")
		}
	}

	results := make(chan []pkgtools.Tool, 1)
	go func() { results <- provider.ToolsForContext(context.Background(), "u1", "a1") }()
	timer := time.NewTimer(2 * provider.timeout)
	defer timer.Stop()
	select {
	case tools := <-results:
		if len(tools) != 1 || tools[0].Definition().Name != "mcp__healthy__available" {
			t.Fatalf("tools = %#v, want only healthy server tool", tools)
		}
	case <-timer.C:
		t.Fatal("discovery exceeded its shared deadline window")
	}
	if got := deadlineCalls.Load(); got != 5 {
		t.Fatalf("connect/list deadline observations = %d, want 5", got)
	}
	if got := cancelled.Load(); got != 2 {
		t.Fatalf("blocked calls observing cancellation = %d, want 2", got)
	}
}

func TestToolProviderClosesClientWhenListToolsFails(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	db := newFakeDB()
	db.forCtx = []sqlc.McpServer{{
		ID: "srv1", Scope: ScopeUser, UserID: pgnull.Text("u1"), Name: "broken", Url: "https://mcp.example.com",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: now,
	}}
	client := &fakeMCPClient{listErr: errors.New("list failed")}
	provider := NewToolProvider(NewService(db, newFakeVault()))
	provider.connect = func(context.Context, Registration, string) (mcpClient, error) { return client, nil }

	if tools := provider.ToolsForContext(context.Background(), "u1", "a1"); len(tools) != 0 {
		t.Fatalf("tools = %d, want none after list failure", len(tools))
	}
	if got := client.closeCount.Load(); got != 1 {
		t.Fatalf("failed discovery close count = %d, want 1", got)
	}
	if got := client.closeCalls.Load(); got != 1 {
		t.Fatalf("failed discovery Close calls = %d, want 1", got)
	}
}

func TestToolProviderSuccessfulDiscoveryDefersClientCloseToToolProxies(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	db := newFakeDB()
	db.forCtx = []sqlc.McpServer{{
		ID: "srv1", Scope: ScopeUser, UserID: pgnull.Text("u1"), Name: "healthy", Url: "https://mcp.example.com",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: now,
	}}
	client := &fakeMCPClient{tools: []*mcpsdk.Tool{
		{Name: "one", InputSchema: map[string]any{"type": "object"}},
		{Name: "two", InputSchema: map[string]any{"type": "object"}},
	}}
	provider := NewToolProvider(NewService(db, newFakeVault()))
	provider.connect = func(context.Context, Registration, string) (mcpClient, error) { return client, nil }

	tools := provider.ToolsForContext(context.Background(), "u1", "a1")
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(tools))
	}
	if got := client.closeCount.Load(); got != 0 {
		t.Fatalf("successful discovery Close calls = %d, want 0", got)
	}
	for _, tool := range tools {
		proxy, ok := tool.(*toolProxy)
		if !ok {
			t.Fatalf("tool proxy = %T, want *toolProxy", tool)
		}
		if err := proxy.Close(); err != nil {
			t.Fatalf("close tool proxy: %v", err)
		}
	}
	if got := client.closeCount.Load(); got != 1 {
		t.Fatalf("shared client close count = %d, want 1", got)
	}
	if got := client.closeCalls.Load(); got != 2 {
		t.Fatalf("shared client Close calls = %d, want 2 tool proxy closes", got)
	}
}

func TestToolProviderSkipsCollidingToolNames(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	db := newFakeDB()
	db.forCtx = []sqlc.McpServer{
		{ID: "srv1", Scope: ScopeUser, UserID: pgnull.Text("u1"), Name: "git-hub", Url: "https://mcp.example.com", Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: now},
		{ID: "srv2", Scope: ScopeUser, UserID: pgnull.Text("u1"), Name: "git_hub", Url: "https://mcp.example.com", Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: now},
	}
	provider := NewToolProvider(NewService(db, newFakeVault()))
	var clients sync.Map
	provider.connect = func(_ context.Context, reg Registration, _ string) (mcpClient, error) {
		client := &fakeMCPClient{tools: []*mcpsdk.Tool{{Name: "foo-bar", InputSchema: map[string]any{"type": "object"}}}}
		clients.Store(reg.ID, client)
		return client, nil
	}

	tools := provider.ToolsForContext(context.Background(), "u1", "a1")
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want one duplicate skipped", len(tools))
	}
	if got := tools[0].Definition().Name; got != "mcp__git_hub__foo_bar" {
		t.Fatalf("tool name = %q", got)
	}
	retained, retainedOK := clients.Load("srv1")
	shadowed, shadowedOK := clients.Load("srv2")
	if !retainedOK || !shadowedOK {
		t.Fatalf("connected clients missing: retained=%v shadowed=%v", retainedOK, shadowedOK)
	}
	if got := retained.(*fakeMCPClient).closeCount.Load(); got != 0 {
		t.Fatalf("retained server close count = %d, want 0 before registry teardown", got)
	}
	if got := shadowed.(*fakeMCPClient).closeCount.Load(); got != 1 {
		t.Fatalf("fully shadowed server close count = %d, want 1", got)
	}
	if err := tools[0].(*toolProxy).Close(); err != nil {
		t.Fatalf("close retained proxy: %v", err)
	}
}

func TestToolProviderCacheExpires(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	db := newFakeDB()
	db.forCtx = []sqlc.McpServer{{
		ID: "srv1", Scope: ScopeUser, UserID: pgnull.Text("u1"), Name: "gh", Url: "https://mcp.example.com",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeNone, Enabled: true, UpdatedAt: now,
	}}
	provider := NewToolProvider(NewService(db, newFakeVault()))
	provider.now = func() time.Time { return now }
	provider.ttl = time.Minute

	connects := 0
	provider.connect = func(context.Context, Registration, string) (mcpClient, error) {
		connects++
		return &fakeMCPClient{tools: []*mcpsdk.Tool{{Name: "create_issue", InputSchema: map[string]any{"type": "object"}}}}, nil
	}

	_ = provider.ToolsForContext(context.Background(), "u1", "a1")
	now = now.Add(time.Minute)
	_ = provider.ToolsForContext(context.Background(), "u1", "a1")

	if connects != 2 {
		t.Fatalf("connects after TTL expiry = %d, want 2", connects)
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
