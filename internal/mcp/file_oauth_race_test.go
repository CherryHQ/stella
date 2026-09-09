package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/CherryHQ/stella/internal/authz"
)

func TestFileOAuthRefreshDisconnectRace(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userID, _ := setupInternal(t)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), true)
	if err != nil {
		t.Fatal(err)
	}

	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseResponse) }) }
	const (
		accessToken  = "stale-file-access-token"
		refreshToken = "file-refresh-token"
		clientSecret = "file-client-secret"
	)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("token request method = %q, want POST", r.Method)
		}
		close(requestStarted)
		<-releaseResponse
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"late-file-access-token","refresh_token":"late-file-refresh-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenServer.Close)
	// Cleanup runs LIFO: release the blocked handler before Server.Close waits
	// for it to finish. The Once also makes the normal path and cleanup safe.
	t.Cleanup(release)

	resource := fileOAuthResource("refresh-race", tokenServer.URL)
	reg, err := RegistrationFromFileResource(resource, "", authority)
	if err != nil {
		t.Fatalf("file registration: %v", err)
	}
	owner, err := FileCredentialOwner(reg, authority)
	if err != nil {
		t.Fatalf("file credential owner: %v", err)
	}
	generation, err := svc.prepareFileOAuthGrant(t.Context(), reg, owner)
	if err != nil {
		t.Fatalf("prepare file OAuth grant: %v", err)
	}
	if _, err := svc.storeFileClientStateIfEmpty(t.Context(), reg, fileOAuthClientState{
		ClientID: "file-client-id", ClientSecret: clientSecret, AuthStyle: int(oauth2.AuthStyleInHeader),
	}); err != nil {
		t.Fatalf("store file OAuth client state: %v", err)
	}
	if err := svc.storeFileBundleCAS(t.Context(), reg, owner, OAuthBundle{
		Version: 1, Generation: generation, ClientID: "file-client-id", TokenEndpoint: tokenServer.URL,
		AuthStyle: int(oauth2.AuthStyleInHeader), AccessToken: accessToken, RefreshToken: refreshToken,
		AccessExpiresAt: time.Now().UTC().Add(-time.Hour),
	}, nil); err != nil {
		t.Fatalf("store expired file OAuth bundle: %v", err)
	}

	source := &oauthRefreshSource{svc: svc, reg: reg, owner: owner}
	tokenDone := make(chan struct{})
	var tokenErr error
	go func() {
		_, tokenErr = source.Token()
		close(tokenDone)
	}()
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	select {
	case <-requestStarted:
	case <-waitCtx.Done():
		t.Fatalf("waiting for /token request: %v", waitCtx.Err())
	}

	if err := svc.DisconnectFile(t.Context(), reg, authority); err != nil {
		t.Fatalf("disconnect file OAuth grant: %v", err)
	}
	release()
	select {
	case <-tokenDone:
	case <-waitCtx.Done():
		t.Fatalf("waiting for refresh result: %v", waitCtx.Err())
	}
	if tokenErr == nil {
		t.Fatal("refresh succeeded after DisconnectFile")
	}
	if !FileMCPGrantRevoked(tokenErr) {
		t.Fatalf("refresh error = %v, want file grant revocation", tokenErr)
	}
	for _, secret := range []string{accessToken, refreshToken, clientSecret, "late-file-access-token", "late-file-refresh-token"} {
		if strings.Contains(tokenErr.Error(), secret) {
			t.Fatalf("refresh error leaked %q: %v", secret, tokenErr)
		}
	}

	raw, err := svc.vault.GetScoped(t.Context(), owner.Scope, owner.UserID, owner.AgentID, fileGrantName(reg, owner))
	if err != nil {
		t.Fatalf("read revoked file grant: %v", err)
	}
	var grant fileGrant
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		t.Fatalf("decode revoked file grant: %v", err)
	}
	if !grant.Revoked {
		t.Fatalf("file grant revoked = false, want true: %s", raw)
	}
	if grant.Bundle != nil {
		t.Fatalf("revoked file grant retained OAuth bundle: %+v", grant.Bundle)
	}
	if ready, err := svc.FileCredentialReady(t.Context(), reg, authority); !errors.Is(err, errFileMCPGrantRevoked) || ready {
		t.Fatalf("file credential readiness after disconnect = %v, %v", ready, err)
	}
	if state := svc.OAuthState(t.Context(), reg, userID); state.Connected {
		t.Fatal("OAuth state remained connected after DisconnectFile")
	}
}

func TestFileOAuthRefreshErrorIsRedacted(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userID, _ := setupInternal(t)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), true)
	if err != nil {
		t.Fatal(err)
	}
	const (
		accessToken    = "stale-file-error-access-token"
		refreshToken   = "file-error-refresh-token"
		clientSecret   = "file-error-client-secret"
		errorBodyToken = "oauth-refresh-error-client-secret-marker"
	)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"` + errorBodyToken + `"}`))
	}))
	t.Cleanup(tokenServer.Close)
	reg, err := RegistrationFromFileResource(fileOAuthResource("refresh-redaction", tokenServer.URL), "", authority)
	if err != nil {
		t.Fatalf("file registration: %v", err)
	}
	owner, err := FileCredentialOwner(reg, authority)
	if err != nil {
		t.Fatalf("file credential owner: %v", err)
	}
	generation, err := svc.prepareFileOAuthGrant(t.Context(), reg, owner)
	if err != nil {
		t.Fatalf("prepare file OAuth grant: %v", err)
	}
	if _, err := svc.storeFileClientStateIfEmpty(t.Context(), reg, fileOAuthClientState{
		ClientID: "file-error-client-id", ClientSecret: clientSecret, AuthStyle: int(oauth2.AuthStyleInHeader),
	}); err != nil {
		t.Fatalf("store file OAuth client state: %v", err)
	}
	if err := svc.storeFileBundleCAS(t.Context(), reg, owner, OAuthBundle{
		Version: 1, Generation: generation, ClientID: "file-error-client-id", TokenEndpoint: tokenServer.URL,
		AuthStyle: int(oauth2.AuthStyleInHeader), AccessToken: accessToken, RefreshToken: refreshToken,
		AccessExpiresAt: time.Now().UTC().Add(-time.Hour),
	}, nil); err != nil {
		t.Fatalf("store expired file OAuth bundle: %v", err)
	}

	_, err = (&oauthRefreshSource{svc: svc, reg: reg, owner: owner}).Token()
	if err == nil {
		t.Fatal("refresh with an error response succeeded")
	}
	for _, secret := range []string{errorBodyToken, accessToken, refreshToken, clientSecret} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("refresh error leaked %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "file OAuth refresh failed") {
		t.Fatalf("refresh error = %v, want bounded public error", err)
	}
}

func TestFileOAuthExchangeErrorIsRedacted(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userID, _ := setupInternal(t)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), true)
	if err != nil {
		t.Fatal(err)
	}
	as := newFakeAS(t)
	const errorBodyToken = "oauth-exchange-error-client-secret-marker"
	as.tokenStatus = http.StatusBadRequest
	as.tokenBody = `{"error":"invalid_grant","error_description":"` + errorBodyToken + `"}`
	mcpSrv := fakeMCPServer(t, as.ts.URL)
	as.resource = mcpSrv.URL
	reg, err := RegistrationFromFileResource(fileOAuthResource("exchange-redaction", mcpSrv.URL), "", authority)
	if err != nil {
		t.Fatalf("file registration: %v", err)
	}
	authURL, flowID, _, err := svc.StartOAuthForAuthority(t.Context(), reg, authority, "http://192.0.2.10/api/mcp/oauth/callback")
	if err != nil {
		t.Fatalf("start file OAuth: %v", err)
	}
	_, challenge, _ := parseAuthCodeURL(t, authURL)
	as.setExpectedChallenge(challenge)
	if _, err := svc.CompleteOAuth(t.Context(), flowID, "authorization-code"); err == nil {
		t.Fatal("exchange with an error response succeeded")
	} else {
		for _, secret := range []string{errorBodyToken, "authorization-code", "dcr-secret", "new-access", "new-refresh"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("exchange error leaked %q: %v", secret, err)
			}
		}
		if !strings.Contains(err.Error(), "file OAuth authorization failed") {
			t.Fatalf("exchange error = %v, want bounded public error", err)
		}
	}
	if got := as.tokenHits.Load(); got != 1 {
		t.Fatalf("/token hits = %d, want 1", got)
	}
}
