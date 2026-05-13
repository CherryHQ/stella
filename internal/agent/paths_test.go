package agent

import (
	"testing"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
)

func TestResolvePathsDefaultsWorkDirToUserRoot(t *testing.T) {
	paths, err := sandbox.ResolvePaths(sandbox.Config{
		Paths: sandbox.PathConfig{
			AgentRoot: "/workspace/agent",
			UserRoot:  "/workspace/agent/users/1",
		},
	})
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	if paths.UserRoot != "/workspace/agent/users/1" {
		t.Fatalf("UserRoot = %q, want %q", paths.UserRoot, "/workspace/agent/users/1")
	}
}
