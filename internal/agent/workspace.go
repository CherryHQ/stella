package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// SetupWorkspace ensures the per-agent workspace directory exists.
// Creates: basePath/workspaces/{agentID}/.agents/skills/
// Returns the absolute path to the agent's workspace directory.
func SetupWorkspace(agentID, basePath string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("agent ID must not be empty")
	}
	dir := filepath.Join(basePath, "workspaces", agentID)
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "skills"), 0o755); err != nil {
		return "", fmt.Errorf("create workspace for agent %q: %w", agentID, err)
	}
	return dir, nil
}

// SetupUserWorkspace ensures per-user directories exist within an agent workspace.
// Creates:
//   - basePath/workspaces/{agentID}/users/{userID}/.agents/skills/
//   - basePath/workspaces/{agentID}/users/{userID}/data/
//   - basePath/workspaces/{agentID}/users/{userID}/assets/
//
// Returns the absolute path to the user's workspace directory
// (basePath/workspaces/{agentID}/users/{userID}/).
func SetupUserWorkspace(agentID, basePath string, userID int64) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("agent ID must not be empty")
	}
	if userID <= 0 {
		return "", fmt.Errorf("user ID must be positive")
	}
	userDir := filepath.Join(basePath, "workspaces", agentID, "users", fmt.Sprintf("%d", userID))
	if err := os.MkdirAll(filepath.Join(userDir, ".agents", "skills"), 0o755); err != nil {
		return "", fmt.Errorf("create user workspace for agent %q user %d: %w", agentID, userID, err)
	}
	if err := os.MkdirAll(filepath.Join(userDir, "data"), 0o755); err != nil {
		return "", fmt.Errorf("create user data dir for agent %q user %d: %w", agentID, userID, err)
	}
	if err := os.MkdirAll(filepath.Join(userDir, "assets"), 0o755); err != nil {
		return "", fmt.Errorf("create user assets dir for agent %q user %d: %w", agentID, userID, err)
	}
	return userDir, nil
}

// UserAssetsDir returns the per-user assets directory within a user root.
// Uploaded files from all channels are stored here.
func UserAssetsDir(userRoot string) string {
	return filepath.Join(userRoot, "assets")
}

// UserSkillsDir returns the per-user skills directory path within a user workspace.
func UserSkillsDir(userWorkspace string) string {
	return filepath.Join(userWorkspace, ".agents", "skills")
}

// UserRoot returns the per-user writable root path within a user workspace.
//
// User-owned runtime data, skills, and presets all live under this root:
//   - users/{id}/data/
//   - users/{id}/.agents/skills/
//   - users/{id}/.agents/agents/
func UserRoot(userWorkspace string) string {
	return userWorkspace
}
