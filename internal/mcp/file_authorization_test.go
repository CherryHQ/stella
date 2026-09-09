package mcp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/plugin"
)

func fileOAuthResource(name, endpoint string) plugin.FileResource {
	return plugin.FileResource{
		Key: plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: name},
		MCP: map[string]mcpconfig.Declaration{
			name: {URL: endpoint, Transport: TransportStreamableHTTP, Authentication: mcpconfig.Authentication{Type: AuthTypeOAuth, Mode: CredentialModePerUser}},
		},
	}
}

func TestFileMCPSharedOAuthRequiresAdminBeforeDiscovery(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userID, _ := setupInternal(t)
	var discoveryHits atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		discoveryHits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer endpoint.Close()
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	resource := plugin.FileResource{
		Key: plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "shared-file"},
		MCP: map[string]mcpconfig.Declaration{
			"shared-file": {
				URL: endpoint.URL, Transport: TransportStreamableHTTP,
				Authentication: mcpconfig.Authentication{Type: AuthTypeOAuth, Mode: CredentialModeShared},
			},
		},
	}
	reg, err := RegistrationFromFileResource(resource, "", authority)
	if err != nil {
		t.Fatalf("file registration: %v", err)
	}
	if _, _, _, err := svc.StartOAuthForAuthority(t.Context(), reg, authority, "http://192.0.2.10/api/mcp/oauth/callback"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("non-admin shared OAuth error = %v, want forbidden", err)
	}
	if got := discoveryHits.Load(); got != 0 {
		t.Fatalf("discovery requests = %d, want 0", got)
	}
}

func TestFileMCPAuthorization(t *testing.T) {
	withLoopbackDialer(t)
	svc, _, userA, _ := setupInternal(t)
	userB := newUserForOAuthTest(t, svc.pool)
	as := newFakeAS(t)
	mcpSrv := fakeMCPServer(t, as.ts.URL)
	as.resource = mcpSrv.URL
	authorityA, err := authz.NewUserAuthority(authz.UserID(userA), true)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := RegistrationFromFileResource(fileOAuthResource("shared-file", mcpSrv.URL), "", authorityA)
	if err != nil {
		t.Fatalf("file registration: %v", err)
	}
	if !reg.IsFile() || reg.ID == "" || reg.AuthenticationTarget == "" {
		t.Fatalf("file identity not marked: %+v", reg)
	}
	regSame, err := RegistrationFromFileResource(fileOAuthResource("shared-file", mcpSrv.URL), "", authorityA)
	if err != nil || regSame.ID != reg.ID {
		t.Fatalf("same file target identity changed: %q vs %q, err=%v", reg.ID, regSame.ID, err)
	}
	changed := fileOAuthResource("shared-file", mcpSrv.URL+"/changed")
	regChanged, err := RegistrationFromFileResource(changed, "", authorityA)
	if err != nil || regChanged.ID == reg.ID {
		t.Fatalf("changed endpoint reused file identity: %q vs %q, err=%v", reg.ID, regChanged.ID, err)
	}
	// The same trusted system declaration has one grant per user.
	authURL, flowID, _, err := svc.StartOAuthForAuthority(t.Context(), reg, authorityA, "http://192.0.2.10/api/mcp/oauth/callback")
	if err != nil {
		t.Fatalf("start file OAuth: %v", err)
	}
	_, challenge, _ := parseAuthCodeURL(t, authURL)
	as.setExpectedChallenge(challenge)
	if ready, err := svc.FileCredentialReady(t.Context(), reg, authorityA); err != nil || ready {
		t.Fatalf("A readiness before callback = %v, %v", ready, err)
	}
	authorityB, err := authz.NewUserAuthority(authz.UserID(userB), false)
	if err != nil {
		t.Fatal(err)
	}
	if ready, err := svc.FileCredentialReady(t.Context(), reg, authorityB); err != nil || ready {
		t.Fatalf("B readiness before callback = %v, %v", ready, err)
	}
	if _, err := svc.CompleteOAuth(t.Context(), flowID, "authorization-code"); err != nil {
		t.Fatalf("complete file OAuth: %v", err)
	}
	if ready, err := svc.FileCredentialReady(t.Context(), reg, authorityA); err != nil || !ready {
		t.Fatalf("A readiness after callback = %v, %v", ready, err)
	}
	if ready, err := svc.FileCredentialReady(t.Context(), reg, authorityB); err != nil || ready {
		t.Fatalf("B readiness after A callback = %v, %v", ready, err)
	}
	ownerB, err := FileCredentialOwner(reg, authorityB)
	if err != nil || ownerB.UserID != userB || ownerB.Scope != ScopeUser {
		t.Fatalf("B owner = %+v, err=%v", ownerB, err)
	}
	// A second flow captures a fresh generation. Disconnect rotates it before
	// the callback, so the callback cannot recreate the grant.
	secondURL, secondFlow, _, err := svc.StartOAuthForAuthority(t.Context(), reg, authorityA, "http://192.0.2.10/api/mcp/oauth/callback")
	if err != nil {
		t.Fatalf("start second file OAuth: %v", err)
	}
	_, secondChallenge, _ := parseAuthCodeURL(t, secondURL)
	as.setExpectedChallenge(secondChallenge)
	if err := svc.DisconnectFile(t.Context(), reg, authorityA); err != nil {
		t.Fatalf("disconnect file OAuth: %v", err)
	}
	if _, err := svc.CompleteOAuth(t.Context(), secondFlow, "authorization-code"); !FileMCPGrantRevoked(err) {
		t.Fatalf("late callback error = %v, want file grant revocation", err)
	}
}
