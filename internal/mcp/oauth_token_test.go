package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	credoauth "github.com/CherryHQ/stella/internal/connections/oauth"
)

// preloadBundle writes a bundle whose access token is already expired so the
// next Token() call must refresh through the fake AS.
func preloadBundle(t *testing.T, svc *Service, reg Registration, userID, tokenEndpoint string) {
	t.Helper()
	bundle := credoauth.OAuthBundle{
		Version: 1, ClientID: "dcr-client-1", TokenEndpoint: tokenEndpoint,
		AccessToken: "stale-access", RefreshToken: "refresh-1",
		AccessExpiresAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := svc.oauthTokens.Save(context.Background(), oauthBundleRef(reg, svc.CredentialOwner(reg, userID)), bundle); err != nil {
		t.Fatalf("preload bundle: %v", err)
	}
}

func TestOAuthRefreshSingleFlight(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userID, _ := setupInternal(t)
	as := newFakeAS(t)
	mcpSrv := fakeMCPServer(t, as.ts.URL)
	as.resource = mcpSrv.URL
	reg := seedOAuthRegistration(t, svc.pool, ScopeUser, userID, "", mcpSrv.URL)
	preloadBundle(t, svc, reg, userID, as.ts.URL+"/token")

	handler := &oauthSession{svc: svc, reg: reg, owner: svc.CredentialOwner(reg, userID)}
	ts, err := handler.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}

	const goroutines = 10
	var wg sync.WaitGroup
	var first *string
	var mu sync.Mutex
	for range goroutines {
		wg.Go(func() {
			tok, err := ts.Token()
			if err != nil {
				t.Errorf("Token: %v", err)
				return
			}
			mu.Lock()
			if first == nil {
				first = &tok.AccessToken
			}
			mu.Unlock()
		})
	}
	wg.Wait()

	if got := as.tokenHits.Load(); got != 1 {
		t.Fatalf("/token hits = %d, want exactly 1 under concurrency", got)
	}
	if first == nil || *first != "new-access" {
		t.Fatalf("token = %v, want the refreshed access token", first)
	}
	bundle, err := svc.oauthTokens.Load(context.Background(), oauthBundleRef(reg, svc.CredentialOwner(reg, userID)))
	if err != nil || bundle == nil || bundle.AccessToken != "new-access" {
		t.Fatalf("refreshed bundle not persisted: %v, %v", bundle, err)
	}
}

func TestOAuthRefreshInvalidGrantFailsClosed(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userID, _ := setupInternal(t)
	as := newFakeAS(t)
	as.tokenStatus = http.StatusBadRequest
	as.tokenBody = `{"error":"invalid_grant"}`
	mcpSrv := fakeMCPServer(t, as.ts.URL)
	as.resource = mcpSrv.URL
	reg := seedOAuthRegistration(t, svc.pool, ScopeUser, userID, "", mcpSrv.URL)
	preloadBundle(t, svc, reg, userID, as.ts.URL+"/token")

	handler := &oauthSession{svc: svc, reg: reg, owner: svc.CredentialOwner(reg, userID)}
	ts, err := handler.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if _, err := ts.Token(); err == nil {
		t.Fatal("invalid_grant must fail the token source")
	}
	fresh := registrationFromRow(mustGetRow(t, svc.pool, reg.ID))
	if fresh.Status != StatusNeedsAuth || fresh.StatusError != credentialRejectedHint {
		t.Fatalf("refresh failure status = %q error %q, want durable reconnect state", fresh.Status, fresh.StatusError)
	}
	// The Authorize path is always non-nil, so the transport's single retry can
	// never loop; the status is durable needs_auth.
	status := http.StatusUnauthorized
	req, _ := http.NewRequest("GET", reg.URL, nil)
	resp := &http.Response{StatusCode: status, Header: http.Header{"Www-Authenticate": {`Bearer error="invalid_token"`}}, Body: http.NoBody}
	if err := handler.Authorize(context.Background(), req, resp); err == nil || !contains(err.Error(), credentialRejectedHint) {
		t.Fatalf("Authorize error = %v, want the reconnect hint", err)
	}
	fresh = registrationFromRow(mustGetRow(t, svc.pool, reg.ID))
	if fresh.Status != StatusNeedsAuth {
		t.Fatalf("status = %q, want needs_auth", fresh.Status)
	}
	if !contains(fresh.StatusError, "invalid_token") || contains(fresh.StatusError, "new-access") {
		t.Fatalf("status_error = %q, want the bounded challenge code only", fresh.StatusError)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestOAuthSSRFPrivateMetadata proves an AS metadata pointer at a private IP
// (from a malicious WWW-Authenticate) never gets dialed: the loopback test
// hook routes everything else through the production SSRF dialer, which
// rejects the private address before any connection.
func TestOAuthSSRFPrivateMetadata(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userID, _ := setupInternal(t)
	// The "MCP server" is the private-IP target itself: the challenge GET is
	// answered here, but its resource_metadata points at another private IP.
	evil := httptestPrivateServer(t, `Bearer resource_metadata="http://10.0.0.1:1/.well-known/oauth-protected-resource"`)
	reg := seedOAuthRegistration(t, svc.pool, ScopeUser, userID, "", evil)
	if _, _, _, err := svc.StartOAuth(context.Background(), reg, userID, "http://192.0.2.10/auth/callback/mcp"); err == nil {
		t.Fatal("StartOAuth must fail when metadata points at a private IP")
	}
}

// httptestPrivateServer serves one fixed WWW-Authenticate challenge.
func httptestPrivateServer(t *testing.T, challenge string) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", challenge)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestOAuthNoSecretsInProjection(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userID, _ := setupInternal(t)
	as := newFakeAS(t)
	mcpSrv := fakeMCPServer(t, as.ts.URL)
	as.resource = mcpSrv.URL
	reg := seedOAuthRegistration(t, svc.pool, ScopeUser, userID, "", mcpSrv.URL)
	preloadBundle(t, svc, reg, userID, as.ts.URL+"/token")

	state := svc.OAuthState(context.Background(), reg, userID)
	if !state.Connected {
		t.Fatal("state.Connected = false, want true with a bundle present")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"stale-access", "refresh-1", "new-access", "dcr-secret"} {
		if contains(string(raw), secret) {
			t.Fatalf("oauth projection leaked %q: %s", secret, raw)
		}
	}
}
