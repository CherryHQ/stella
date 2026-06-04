package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
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
func SetupUserWorkspace(agentID, basePath string, userID string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("agent ID must not be empty")
	}
	if userID == "" {
		return "", fmt.Errorf("user ID must not be empty")
	}
	userDir := filepath.Join(basePath, "workspaces", agentID, "users", userID)
	if err := os.MkdirAll(filepath.Join(userDir, ".agents", "skills"), 0o755); err != nil {
		return "", fmt.Errorf("create user workspace for agent %q user %s: %w", agentID, userID, err)
	}
	if err := os.MkdirAll(filepath.Join(userDir, "data"), 0o755); err != nil {
		return "", fmt.Errorf("create user data dir for agent %q user %s: %w", agentID, userID, err)
	}
	if err := os.MkdirAll(filepath.Join(userDir, "assets"), 0o755); err != nil {
		return "", fmt.Errorf("create user assets dir for agent %q user %s: %w", agentID, userID, err)
	}
	return userDir, nil
}

// SetupGroupWorkspace ensures per-group directories exist within an agent workspace.
// Returns the path basePath/workspaces/{agentID}/groups/{groupID}/.
func SetupGroupWorkspace(agentID, basePath, groupID string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("agent ID must not be empty")
	}
	if groupID == "" {
		return "", fmt.Errorf("group ID must not be empty")
	}
	dir := filepath.Join(basePath, "workspaces", agentID, "groups", groupID)
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "skills"), 0o755); err != nil {
		return "", fmt.Errorf("create group workspace for agent %q group %s: %w", agentID, groupID, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		return "", fmt.Errorf("create group data dir for agent %q group %s: %w", agentID, groupID, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		return "", fmt.Errorf("create group assets dir for agent %q group %s: %w", agentID, groupID, err)
	}
	return dir, nil
}

// SetupSystemWorkspace creates the shared system workspace for agent jobs that
// run without a user context (e.g. builtin scheduled jobs).
// Returns the path basePath/workspaces/{agentID}/system/.
func SetupSystemWorkspace(agentID, basePath string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("agent ID must not be empty")
	}
	dir := filepath.Join(basePath, "workspaces", agentID, "system")
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "skills"), 0o755); err != nil {
		return "", fmt.Errorf("create system workspace for agent %q: %w", agentID, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		return "", fmt.Errorf("create system data dir for agent %q: %w", agentID, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		return "", fmt.Errorf("create system assets dir for agent %q: %w", agentID, err)
	}
	return dir, nil
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

// SaveAsset writes data to assetsDir with a timestamp-prefixed filename to avoid
// collisions, returning the absolute path of the saved file.
func SaveAsset(assetsDir, fileName string, data []byte) (string, error) {
	name := fmt.Sprintf("%d_%s", time.Now().UnixNano(), filepath.Base(fileName))
	dst := filepath.Join(assetsDir, name)
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", fmt.Errorf("write asset %s: %w", name, err)
	}
	return dst, nil
}
