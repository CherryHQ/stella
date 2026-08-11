package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/skills"
)

// ProjectDescriptor is the authorized logical identity of a project. Path is a
// canonical agent-workspace-relative name; it is never a provider/host path.
type ProjectDescriptor struct {
	ID      string
	UserID  string
	AgentID string
	Path    string
}

// ProjectResolverFunc resolves the exact (user, agent, project) tuple without
// exposing the workspace provider's physical path.
type ProjectResolverFunc func(ctx context.Context, projectID, userID, agentID string) (ProjectDescriptor, error)

// SnapshotAuthorizedProjectContext resolves the exact project authority, opens
// a read-only Agent workspace capability, snapshots bounded root-to-leaf
// AGENTS.md context, and closes the capability before prompt construction.
func SnapshotAuthorizedProjectContext(ctx context.Context, resolve ProjectResolverFunc, opener home.RootOpener, projectID, userID, agentID string) (prompt.ProjectContext, ProjectDescriptor, error) {
	if resolve == nil || opener == nil {
		return prompt.ProjectContext{}, ProjectDescriptor{}, errors.New("project context authority is unavailable")
	}
	d, err := resolve(ctx, projectID, userID, agentID)
	if err != nil {
		return prompt.ProjectContext{}, ProjectDescriptor{}, err
	}
	if d.ID != projectID || d.UserID != userID || d.AgentID != agentID {
		return prompt.ProjectContext{}, ProjectDescriptor{}, errors.New("project descriptor authority mismatch")
	}
	if _, err := canonicalProjectPath(d.Path); err != nil {
		return prompt.ProjectContext{}, ProjectDescriptor{}, errors.New("project descriptor path is invalid")
	}
	root, err := opener.OpenRoot(ctx, home.WorkspaceRequest{UserID: userID, AgentID: agentID}, home.RootAgentWorkspace, home.RootReadOnly)
	if err != nil {
		return prompt.ProjectContext{}, ProjectDescriptor{}, err
	}
	snapshot, err := prompt.SnapshotProjectContext(ctx, root, d.Path)
	if err != nil {
		return prompt.ProjectContext{}, ProjectDescriptor{}, err
	}
	return snapshot, d, nil
}

// SnapshotAuthorizedProjectSkills resolves the exact project authority, opens a
// read-only workspace capability, snapshots Skills, and closes the capability
// before returning it to downstream prompt/tool consumers.
func SnapshotAuthorizedProjectSkills(ctx context.Context, resolve ProjectResolverFunc, opener home.RootOpener, projectID, userID, agentID string) (*skills.ProjectSnapshot, ProjectDescriptor, error) {
	if resolve == nil || opener == nil {
		return nil, ProjectDescriptor{}, errors.New("project skill authority is unavailable")
	}
	d, err := resolve(ctx, projectID, userID, agentID)
	if err != nil {
		return nil, ProjectDescriptor{}, err
	}
	if d.ID != projectID || d.UserID != userID || d.AgentID != agentID {
		return nil, ProjectDescriptor{}, errors.New("project descriptor authority mismatch")
	}
	if _, err := canonicalProjectPath(d.Path); err != nil {
		return nil, ProjectDescriptor{}, errors.New("project descriptor path is invalid")
	}
	root, err := opener.OpenRoot(ctx, home.WorkspaceRequest{UserID: userID, AgentID: agentID}, home.RootAgentWorkspace, home.RootReadOnly)
	if err != nil {
		return nil, ProjectDescriptor{}, err
	}
	snapshot, snapshotErr := skills.SnapshotProjectSkills(ctx, root, d.Path)
	closeErr := root.Close()
	if snapshotErr != nil {
		return nil, ProjectDescriptor{}, snapshotErr
	}
	if closeErr != nil {
		return nil, ProjectDescriptor{}, closeErr
	}
	return snapshot, d, nil
}

// ValidateProjectDir checks that baseDir is a safe subpath of userRoot.
// It rejects paths containing ".." traversal or paths outside the workspace.
func ValidateProjectDir(baseDir, userRoot string) error {
	if baseDir == "" {
		return fmt.Errorf("base_dir must not be empty")
	}
	if !filepath.IsAbs(baseDir) {
		return fmt.Errorf("base_dir must be absolute")
	}
	if !filepath.IsAbs(userRoot) {
		return fmt.Errorf("user_root must be absolute")
	}

	cleanBase := filepath.Clean(baseDir)
	cleanRoot := filepath.Clean(userRoot)

	// Resolve symlinks when paths exist on disk.
	if resolved, err := filepath.EvalSymlinks(cleanRoot); err == nil {
		cleanRoot = resolved
	}
	if resolved, err := filepath.EvalSymlinks(cleanBase); err == nil {
		cleanBase = resolved
	} else {
		// Path doesn't exist yet — resolve the longest existing prefix
		// to catch symlink escapes in parent directories.
		dir := cleanBase
		for dir != "/" && dir != "." {
			parent := filepath.Dir(dir)
			if resolved, err := filepath.EvalSymlinks(parent); err == nil {
				tail, _ := filepath.Rel(parent, cleanBase)
				cleanBase = filepath.Join(resolved, tail)
				break
			}
			dir = parent
		}
	}

	rel, err := filepath.Rel(cleanRoot, cleanBase)
	if err != nil {
		return fmt.Errorf("base_dir is not under user workspace")
	}
	if strings.HasPrefix(rel, "..") {
		return fmt.Errorf("base_dir must be under user workspace")
	}

	return nil
}

// ProjectEnsurerFunc ensures a default project exists for an agent+user pair.
// Returns the project ID. Called when a session is created without a project.
type ProjectEnsurerFunc func(ctx context.Context, agentID, userID string) (projectID string, err error)
