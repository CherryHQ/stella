package agent

import (
	"testing"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
)

func TestResolveRunnerPathsDefaultsWorkDirToUserRoot(t *testing.T) {
	pc := sandbox.PathConfig{
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/agent/users/1",
	}

	paths := resolveRunnerPaths(pc)
	if paths.UserRoot != pc.UserRoot {
		t.Fatalf("UserRoot = %q, want %q", paths.UserRoot, pc.UserRoot)
	}
}
