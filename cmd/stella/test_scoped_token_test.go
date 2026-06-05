package main

import (
	"testing"
	"time"

	pkgauth "github.com/CherryHQ/stella/pkg/auth"
)

func setTestScopedToken(t *testing.T, agentID string) {
	t.Helper()
	tok, err := pkgauth.SignScopedToken([]byte("test-secret"), pkgauth.ScopedTokenClaims{
		UserID:    "user-1",
		AgentID:   agentID,
		SessionID: "session-1",
	}, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("sign scoped token: %v", err)
	}
	t.Setenv("STELLA_TOKEN", tok)
}
