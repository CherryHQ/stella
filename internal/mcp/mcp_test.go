package mcp

import (
	"context"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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
	d.rows[arg.ID] = row
	return row, nil
}

func (d *fakeDB) DeleteMCPServerByScope(_ context.Context, arg sqlc.DeleteMCPServerByScopeParams) error {
	d.deleted = append(d.deleted, arg.ID)
	return nil
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

func TestUpdateBearerCanKeepTokenWhenScopeMoves(t *testing.T) {
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

	updated, err := svc.Update(context.Background(), UpdateInput{
		ID: reg.ID, Scope: ScopeUser, UserID: "u1",
		NewScope: strPtr(ScopeUserAgent), NewUserID: "u1", NewAgentID: "a1",
		URL: strPtr("https://new.example.com"),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Scope != ScopeUserAgent || updated.AgentID != "a1" || updated.URL != "https://new.example.com" {
		t.Fatalf("updated registration = %+v", updated)
	}
	if _, ok := vlt.stored[vaultKey(ScopeUserAgent, "u1", "a1", reg.CredentialRef)]; !ok {
		t.Fatal("token should still exist under the stable credential name in the new scope")
	}
	back, err := svc.BearerToken(context.Background(), updated)
	if err != nil {
		t.Fatalf("BearerToken: %v", err)
	}
	if back != "secret" {
		t.Fatalf("BearerToken = %q, want secret", back)
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
	tools  []*mcpsdk.Tool
	calls  int
	closed bool
}

func (c *fakeMCPClient) ListTools(context.Context) ([]*mcpsdk.Tool, error) {
	c.calls++
	return c.tools, nil
}

func (c *fakeMCPClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "ok", nil
}

func (c *fakeMCPClient) Close() error {
	c.closed = true
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
	if got := NamespacedToolName("git hub", "create_issue"); got != "mcp__git_hub__create_issue" {
		t.Fatalf("NamespacedToolName = %q", got)
	}
}
