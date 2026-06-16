package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/auth"
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
	dir, err := SetupGroupWorkspace(base, "abc-123", "agent1")
	if err != nil {
		t.Fatalf("SetupGroupWorkspace: %v", err)
	}
	// A channel group is a principal in the users tree, keyed under a "group-"
	// prefix so it can't collide with a real user home of the same raw ID (#442).
	want := filepath.Join(base, "users", "group-abc-123")
	if dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	// Verify the shared user-data subtree exists.
	for _, sub := range []string{"data/.agents/skills", "data/.agents/delegates", "data/.mise-tools", "data/assets"} {
		p := filepath.Join(dir, sub)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			t.Fatalf("expected directory %q to exist", p)
		}
	}
}

func TestGroupWorkspaceDisjointFromUserOfSameID(t *testing.T) {
	base := t.TempDir()
	id := "abc-123"
	groupDir, err := SetupGroupWorkspace(base, id, "agent1")
	if err != nil {
		t.Fatalf("SetupGroupWorkspace: %v", err)
	}
	userDir, err := SetupUserWorkspace(base, id, "agent1")
	if err != nil {
		t.Fatalf("SetupUserWorkspace: %v", err)
	}
	// Both live under users/, but the "group-" prefix keeps a group and a real
	// user of the same raw ID from sharing one home.
	if filepath.Dir(groupDir) != filepath.Join(base, "users") {
		t.Fatalf("group workspace must sit directly under users/, got %q", groupDir)
	}
	if groupDir == userDir {
		t.Fatalf("group and user home for the same raw ID must differ, both %q", groupDir)
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
