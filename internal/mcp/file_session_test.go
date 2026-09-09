package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/tools"
)

// fileSessionTools projects the old synthetic registrations used by lifecycle
// tests back through the production resource API. It keeps those tests focused
// on session ownership without retaining a registration-only production seam.
func fileSessionTools(t *testing.T, provider *ToolProvider, ctx context.Context, session *FileSession, registrations []Registration, authority authz.Authority) ([]tools.Tool, error) {
	t.Helper()
	resources := make([]plugin.FileResource, 0, len(registrations))
	for _, reg := range registrations {
		resources = append(resources, plugin.FileResource{
			Key: reg.FileKey,
			MCP: map[string]mcpconfig.Declaration{reg.ServerKey: {
				URL: reg.URL, Transport: reg.Transport, Headers: reg.Headers, CallTimeoutSeconds: reg.CallTimeoutSeconds,
				Authentication: mcpconfig.Authentication{Type: reg.AuthType, Mode: reg.CredentialMode, CredentialRef: reg.CredentialRef, ClientID: reg.OAuthClientID, ClientSecretRef: reg.OAuthClientSecretRef, TokenEndpointAuthMethod: reg.TokenEndpointAuthMethod, Scopes: reg.OAuthScopes},
			}},
		})
	}
	snapshot, err := provider.ToolsForFileSession(ctx, session, resources, authority)
	return snapshot.Tools, err
}

type fileSessionFakeClient struct {
	closeCount atomic.Int32
	listCount  atomic.Int32
	callCount  atomic.Int32
	dirty      func()
}

func (c *fileSessionFakeClient) ListTools(context.Context) ([]*mcpsdk.Tool, error) {
	c.listCount.Add(1)
	return []*mcpsdk.Tool{{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (c *fileSessionFakeClient) CallTool(_ context.Context, _ string, _ map[string]any) (*mcpsdk.CallToolResult, error) {
	c.callCount.Add(1)
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil
}

func (c *fileSessionFakeClient) Close() error {
	c.closeCount.Add(1)
	return nil
}

func fileSessionTestAuthority(t *testing.T, user string) authz.Authority {
	t.Helper()
	authority, err := authz.NewUserAuthority(authz.UserID(user), false)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func fileSessionTestRegistration(id, endpoint string) Registration {
	return Registration{
		IdentityKind: RegistrationIdentityFile,
		FileKey:      plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "weather"},
		ID:           id, ParentConfigID: "weather", ServerKey: "main", PluginID: "file:weather",
		Scope: ScopeSystem, Name: "weather", URL: endpoint, Transport: TransportStreamableHTTP,
		AuthType: AuthTypeNone, CredentialMode: CredentialModePerUser, Enabled: true,
	}
}

func TestFileSessionOwnsConnectionsAndReplacesDirtyTargets(t *testing.T) {
	authority := fileSessionTestAuthority(t, "user-a")
	service := &Service{}
	session := NewFileSession(service)
	var mu sync.Mutex
	var clients []*fileSessionFakeClient
	session.SetConnectForTesting(func(_ context.Context, _ Registration, _ CredentialOwner, dirty func()) (RemoteClient, error) {
		client := &fileSessionFakeClient{dirty: dirty}
		mu.Lock()
		clients = append(clients, client)
		mu.Unlock()
		return client, nil
	})
	provider := NewToolProvider(service)
	registration := fileSessionTestRegistration("file-1", "https://weather.example.test/mcp")
	toolsOne, err := fileSessionTools(t, provider, t.Context(), session, []Registration{registration}, authority)
	if err != nil || len(toolsOne) != 1 {
		t.Fatalf("first turn tools = %d, err = %v", len(toolsOne), err)
	}
	if got, err := toolsOne[0].Execute(authz.WithAuthority(t.Context(), authority), nil); err != nil || got != "ok" {
		t.Fatalf("tools/call = %q, err = %v", got, err)
	}
	if err := toolsOne[0].(interface{ Close() error }).Close(); err != nil {
		t.Fatalf("proxy close: %v", err)
	}
	mu.Lock()
	first := clients[0]
	mu.Unlock()
	if got := first.closeCount.Load(); got != 0 {
		t.Fatalf("proxy Close closed session client %d times", got)
	}

	if _, err := fileSessionTools(t, provider, t.Context(), session, []Registration{registration}, authority); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := first.listCount.Load(); got != 2 {
		t.Fatalf("tools/list count = %d, want 2", got)
	}
	first.dirty()
	if _, err := fileSessionTools(t, provider, t.Context(), session, []Registration{registration}, authority); err != nil {
		t.Fatalf("dirty turn: %v", err)
	}
	if got := first.closeCount.Load(); got != 1 {
		t.Fatalf("dirty connection close count = %d, want 1", got)
	}

	changed := registration
	changed.ID = "file-2"
	changed.URL = "https://weather-new.example.test/mcp"
	if _, err := fileSessionTools(t, provider, t.Context(), session, []Registration{changed}, authority); err != nil {
		t.Fatalf("changed target turn: %v", err)
	}
	mu.Lock()
	second := clients[1]
	third := clients[2]
	mu.Unlock()
	if got := second.closeCount.Load(); got != 1 {
		t.Fatalf("replaced connection close count = %d, want 1", got)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("session close: %v", err)
	}
	if got := third.closeCount.Load(); got != 1 {
		t.Fatalf("session-owned close count = %d, want 1", got)
	}
}

func TestFileMCPToolCalls(t *testing.T) {
	withLoopbackDialer(t)
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "file-test", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "http-ok"}}}, nil
	})
	server.AddTool(&mcpsdk.Tool{Name: "second", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "second-ok"}}}, nil
	})
	baseHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})
	var sessionMu sync.Mutex
	sessionIDs := make(map[string]struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessionID := r.Header.Get("Mcp-Session-Id"); sessionID != "" {
			sessionMu.Lock()
			sessionIDs[sessionID] = struct{}{}
			sessionMu.Unlock()
		}
		baseHandler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	service := &Service{endpoints: EndpointPolicy{AllowPrivate: true}}
	session := NewFileSession(service)
	provider := NewToolProvider(service)
	registration := fileSessionTestRegistration("http-file", httpServer.URL)
	authority := fileSessionTestAuthority(t, "http-user")
	modelTools, err := fileSessionTools(t, provider, t.Context(), session, []Registration{registration}, authority)
	if err != nil || len(modelTools) != 2 {
		var failure *connectionFailure
		if errors.As(err, &failure) {
			t.Logf("fake HTTP connection cause: %v", failure.Unwrap())
		}
		t.Fatalf("tools/list result = %d, err = %v", len(modelTools), err)
	}
	results := make(map[string]string, len(modelTools))
	for _, modelTool := range modelTools {
		proxy := modelTool.(*toolProxy)
		got, callErr := proxy.Execute(authz.WithAuthority(t.Context(), authority), map[string]any{"message": "hello"})
		if callErr != nil {
			t.Fatalf("tools/call %q: %v", proxy.remoteName, callErr)
		}
		results[proxy.remoteName] = got
	}
	if results["echo"] != "http-ok" || results["second"] != "second-ok" {
		t.Fatalf("tools/call results = %#v", results)
	}
	sessionMu.Lock()
	uniqueSessions := len(sessionIDs)
	sessionMu.Unlock()
	if uniqueSessions != 1 {
		t.Fatalf("MCP tools used %d sessions, want one", uniqueSessions)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close HTTP session: %v", err)
	}
}

func TestFileSessionDisconnectClosesBlockedConnect(t *testing.T) {
	service := &Service{}
	session := NewFileSession(service)
	registration := fileSessionTestRegistration("blocked-file", "https://weather.example.test/mcp")
	authority := fileSessionTestAuthority(t, "blocked-user")
	owner, err := FileCredentialOwner(registration, authority)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &fileSessionFakeClient{}
	session.SetConnectForTesting(func(context.Context, Registration, CredentialOwner, func()) (RemoteClient, error) {
		close(started)
		<-release
		return fake, nil
	})
	type borrowResult struct {
		conn *fileConnection
		err  error
	}
	result := make(chan borrowResult, 1)
	go func() {
		conn, borrowErr := session.borrow(t.Context(), registration, authority)
		result <- borrowResult{conn: conn, err: borrowErr}
	}()
	<-started
	if err := service.fileConnections.closeGrant(registration, owner); err != nil {
		t.Fatalf("closeGrant: %v", err)
	}
	close(release)
	got := <-result
	if got.conn != nil || !FileMCPGrantRevoked(got.err) {
		t.Fatalf("blocked borrow = conn %v, err %v, want revoked", got.conn, got.err)
	}
	if got := fake.closeCount.Load(); got != 1 {
		t.Fatalf("late client close count = %d, want 1", got)
	}
	service.fileConnections.mu.Lock()
	remaining := len(service.fileConnections.grants)
	service.fileConnections.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("file connection index has %d grants after disconnect", remaining)
	}
}

func TestFileSessionCloseCancelsInitialization(t *testing.T) {
	service := &Service{}
	session := NewFileSession(service)
	registration := fileSessionTestRegistration("pending-file", "https://weather.example.test/mcp")
	authority := fileSessionTestAuthority(t, "pending-user")
	started := make(chan struct{})
	session.SetConnectForTesting(func(ctx context.Context, _ Registration, _ CredentialOwner, _ func()) (RemoteClient, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	done := make(chan error, 1)
	go func() {
		_, err := session.borrow(t.Context(), registration, authority)
		done <- err
	}()
	<-started
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initialization error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("session close did not cancel initialization")
	}
	service.fileConnections.mu.Lock()
	defer service.fileConnections.mu.Unlock()
	if len(service.fileConnections.grants) != 0 {
		t.Fatal("closed session retained a pending handle")
	}
}

func TestFileSessionSSEConnectionSurvivesDiscoveryCancellation(t *testing.T) {
	withLoopbackDialer(t)
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "file-sse-test", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "sse-ok"}}}, nil
	})
	sseHandler := mcpsdk.NewSSEHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	getReturned := make(chan struct{}, 2)
	var getStarted atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getStarted.Add(1)
		}
		sseHandler.ServeHTTP(w, r)
		if r.Method == http.MethodGet {
			getReturned <- struct{}{}
		}
	}))
	defer httpServer.Close()

	service := &Service{endpoints: EndpointPolicy{AllowPrivate: true}}
	session := NewFileSession(service)
	defer func() { _ = session.Close() }()
	provider := NewToolProvider(service)
	registration := fileSessionTestRegistration("sse-file", httpServer.URL)
	registration.Transport = TransportSSE
	authority := fileSessionTestAuthority(t, "sse-user")
	testCtx, cancelTest := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelTest()
	discoveryCtx, cancelDiscovery := context.WithCancel(testCtx)
	modelTools, err := fileSessionTools(t, provider, discoveryCtx, session, []Registration{registration}, authority)
	if err != nil || len(modelTools) != 1 {
		t.Fatalf("SSE tools/list result = %d, err = %v", len(modelTools), err)
	}
	cancelDiscovery()
	select {
	case <-getReturned:
		t.Fatal("SSE GET ended when discovery context was canceled")
	case <-time.After(100 * time.Millisecond):
	}
	if got := getStarted.Load(); got != 1 {
		t.Fatalf("initial SSE GET count = %d, want 1", got)
	}
	got, err := modelTools[0].Execute(authz.WithAuthority(testCtx, authority), nil)
	if err != nil || got != "sse-ok" {
		t.Fatalf("SSE tools/call result = %q, err = %v", got, err)
	}
	if _, err := fileSessionTools(t, provider, testCtx, session, []Registration{registration}, authority); err != nil {
		t.Fatalf("reuse turn: %v", err)
	}
	if got := getStarted.Load(); got != 1 {
		t.Fatalf("unchanged SSE target opened %d GETs, want 1", got)
	}

	server.AddTool(&mcpsdk.Tool{Name: "second", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "second-ok"}}}, nil
	})
	dirtyDeadline := time.NewTimer(time.Second)
	dirtyTick := time.NewTicker(10 * time.Millisecond)
	dirty := false
	for !dirty {
		session.mu.Lock()
		for _, conn := range session.connections {
			dirty = conn.dirtyState()
		}
		session.mu.Unlock()
		if dirty {
			break
		}
		select {
		case <-dirtyTick.C:
		case <-dirtyDeadline.C:
			dirtyTick.Stop()
			t.Fatal("SSE tools/list_changed did not mark the connection dirty")
		}
	}
	dirtyTick.Stop()
	if !dirtyDeadline.Stop() {
		<-dirtyDeadline.C
	}
	nextTools, err := fileSessionTools(t, provider, testCtx, session, []Registration{registration}, authority)
	if err != nil || len(nextTools) != 2 {
		t.Fatalf("SSE replacement tools/list result = %d, err = %v", len(nextTools), err)
	}
	if got := getStarted.Load(); got != 2 {
		t.Fatalf("dirty SSE target opened %d GETs, want 2", got)
	}
	select {
	case <-getReturned:
	case <-time.After(time.Second):
		t.Fatal("replacement did not close the old SSE GET")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close SSE session: %v", err)
	}
	select {
	case <-getReturned:
	case <-time.After(time.Second):
		t.Fatal("FileSession.Close did not cancel the SSE GET")
	}
}
