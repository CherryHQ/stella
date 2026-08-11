package agent

import (
	"path/filepath"
	"testing"
)

func TestWorkspacePathHelpersArePureAndDeterministic(t *testing.T) {
	base := filepath.Join("srv", "stella")
	if got, want := AgentWorkspaceDir(base, "a1"), filepath.Join(base, "agents", "a1"); got != want {
		t.Fatalf("AgentWorkspaceDir = %q, want %q", got, want)
	}
	user := UserHomeDir(base, "same")
	group := GroupHomeDir(base, "same")
	if user == group {
		t.Fatal("user and group with the same raw ID collided")
	}
	if got, want := UserAgentDir(base, "same", "a1"), filepath.Join(user, "agents", "a1"); got != want {
		t.Fatalf("UserAgentDir = %q, want %q", got, want)
	}
	if got, want := GroupAgentDir(base, "same", "a1"), filepath.Join(group, "agents", "a1"); got != want {
		t.Fatalf("GroupAgentDir = %q, want %q", got, want)
	}
	if got, want := UserAssetsDir(user), filepath.Join(user, "data", "assets"); got != want {
		t.Fatalf("UserAssetsDir = %q, want %q", got, want)
	}
}
