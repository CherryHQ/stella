package auth

import (
	"strings"
	"testing"
	"time"
)

func TestScopedTokenSignVerify(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	secret := []byte("secret")
	tok, err := SignScopedToken(secret, ScopedTokenClaims{UserID: "user-1", AgentID: "agent-1", SessionID: "session-1"}, now)
	if err != nil {
		t.Fatalf("SignScopedToken: %v", err)
	}
	if !strings.HasPrefix(tok, ScopedTokenPrefix) {
		t.Fatalf("token prefix = %q", tok)
	}
	claims, err := VerifyScopedToken(secret, tok, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("VerifyScopedToken: %v", err)
	}
	if claims.UserID != "user-1" || claims.AgentID != "agent-1" || claims.SessionID != "session-1" {
		t.Fatalf("claims = %+v", claims)
	}
	if !claims.HasScope("goals:write") || !claims.HasScope("vault:read") {
		t.Fatalf("default scopes missing expected capabilities: %+v", claims.Scopes)
	}
}

func TestScopedTokenScopes(t *testing.T) {
	claims := ScopedTokenClaims{Scopes: []string{"goals:*", "vault:read"}}
	if !claims.HasScope("goals:read") || !claims.HasScope("goals:write") {
		t.Fatal("wildcard scope should allow resource actions")
	}
	if !claims.HasScope("vault:read") {
		t.Fatal("exact scope should allow matching action")
	}
	if claims.HasScope("vault:write") {
		t.Fatal("exact read scope should not allow write")
	}
}

func TestScopedTokenRejectsTamperAndExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, err := SignScopedToken([]byte("secret"), ScopedTokenClaims{UserID: "user-1", AgentID: "agent-1"}, now)
	if err != nil {
		t.Fatalf("SignScopedToken: %v", err)
	}
	if _, err := VerifyScopedToken([]byte("wrong"), tok, now); err == nil {
		t.Fatal("expected wrong secret to fail")
	}
	if _, err := VerifyScopedToken([]byte("secret"), tok, now.Add(scopedTokenTTL+time.Second)); err == nil {
		t.Fatal("expected expired token to fail")
	}
}
