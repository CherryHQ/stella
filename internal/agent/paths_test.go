package agent

import "testing"

func TestResolveRunnerPathsDefaultsWorkDirToUserRoot(t *testing.T) {
	cfg := GoRunnerConfig{
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/agent/users/1",
	}

	paths := resolveRunnerPaths(cfg)
	if paths.UserRoot != cfg.UserRoot {
		t.Fatalf("UserRoot = %q, want %q", paths.UserRoot, cfg.UserRoot)
	}
}
