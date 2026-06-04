package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
)

func TestBuildGroupSessionKey(t *testing.T) {
	key := BuildGroupSessionKey("agent1", "group-abc-123")
	if key != "agent1:group:group-abc-123" {
		t.Fatalf("got %q, want %q", key, "agent1:group:group-abc-123")
	}
}

func TestGroupSessionKeyOmitsSenderID(t *testing.T) {
	key := BuildGroupSessionKey("agent1", "group-abc-123")
	if strings.Contains(key, "user") || strings.Contains(key, "sender") {
		t.Fatalf("group session key should not contain user/sender id, got %q", key)
	}
}

func TestTwoSendersShareGroupSessionKey(t *testing.T) {
	keyA := BuildGroupSessionKey("agent1", "group-abc-123")
	keyB := BuildGroupSessionKey("agent1", "group-abc-123")
	if keyA != keyB {
		t.Fatalf("two senders in the same group should produce the same session key: %q vs %q", keyA, keyB)
	}
}

func TestSetupGroupWorkspace(t *testing.T) {
	base := t.TempDir()
	dir, err := SetupGroupWorkspace("agent1", base, "group-abc-123")
	if err != nil {
		t.Fatalf("SetupGroupWorkspace: %v", err)
	}
	want := filepath.Join(base, "workspaces", "agent1", "groups", "group-abc-123")
	if dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	// Verify subdirectories exist.
	for _, sub := range []string{".agents/skills", "data", "assets"} {
		p := filepath.Join(dir, sub)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			t.Fatalf("expected directory %q to exist", p)
		}
	}
}

func TestGroupWorkspaceNotUnderUsers(t *testing.T) {
	base := t.TempDir()
	dir, err := SetupGroupWorkspace("agent1", base, "group-abc-123")
	if err != nil {
		t.Fatalf("SetupGroupWorkspace: %v", err)
	}
	if strings.Contains(dir, "/users/") {
		t.Fatalf("group workspace must not be under users/, got %q", dir)
	}
}

func TestGroupScopedToken(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-enough")
	groupID := "group-abc-123"
	tok, err := auth.SignScopedToken(secret, auth.ScopedTokenClaims{
		UserID:  "group:" + groupID,
		AgentID: "agent1",
	}, time.Now())
	if err != nil {
		t.Fatalf("SignScopedToken with group principal: %v", err)
	}
	claims, err := auth.VerifyScopedToken(secret, tok, time.Now())
	if err != nil {
		t.Fatalf("VerifyScopedToken: %v", err)
	}
	if claims.UserID != "group:"+groupID {
		t.Fatalf("claims.UserID = %q, want %q", claims.UserID, "group:"+groupID)
	}
	if claims.Subject != "group:"+groupID {
		t.Fatalf("claims.Subject = %q, want %q", claims.Subject, "group:"+groupID)
	}
}
