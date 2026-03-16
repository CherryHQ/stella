package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// SetupWorkspace ensures the per-agent workspace directory exists.
// Creates: basePath/workspaces/{agentID}/skills/
// Returns the absolute path to the agent's workspace directory.
func SetupWorkspace(agentID, basePath string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("agent ID must not be empty")
	}
	dir := filepath.Join(basePath, "workspaces", agentID)
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		return "", fmt.Errorf("create workspace for agent %q: %w", agentID, err)
	}
	return dir, nil
}
