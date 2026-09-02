package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/skill"
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

// AuthorizedProjectSnapshot is an immutable project generation used by prompt
// construction. Descriptor, context, and Skills come from one resolution and
// one read-only root capability, which is closed before downstream plugin work.
type AuthorizedProjectSnapshot struct {
	Descriptor ProjectDescriptor
	Context    prompt.ProjectContext
	Skills     *skill.ProjectSnapshot
}

// SnapshotAuthorizedProject resolves the exact owner tuple once and snapshots
// all project prompt inputs while one read-only owner gate is held. The named
// return and deferred errors.Join close the root on ordinary errors and panics.
func SnapshotAuthorizedProject(ctx context.Context, resolve ProjectResolverFunc, opener home.RootOpener, projectID, userID, agentID string) (snapshot AuthorizedProjectSnapshot, resultErr error) {
	if resolve == nil || opener == nil {
		return AuthorizedProjectSnapshot{}, errors.New("project snapshot authority is unavailable")
	}
	d, err := resolve(ctx, projectID, userID, agentID)
	if err != nil {
		return AuthorizedProjectSnapshot{}, err
	}
	if d.ID != projectID || d.UserID != userID || d.AgentID != agentID {
		return AuthorizedProjectSnapshot{}, errors.New("project descriptor authority mismatch")
	}
	if scope, name, err := home.ResolveLogicalCoordinate(home.RootAgentWorkspace, d.Path, true); err != nil || scope != home.RootAgentWorkspace || name != d.Path {
		return AuthorizedProjectSnapshot{}, errors.New("project descriptor path is invalid")
	}
	root, err := opener.OpenRoot(ctx, home.WorkspaceRequest{UserID: userID, AgentID: agentID}, home.RootAgentWorkspace, home.RootReadOnly)
	if err != nil {
		return AuthorizedProjectSnapshot{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	contextSnapshot, err := prompt.ReadProjectContext(ctx, root, d.Path)
	if err != nil {
		return AuthorizedProjectSnapshot{}, err
	}
	skillSnapshot, err := skill.SnapshotProjectSkills(ctx, root, d.Path)
	if err != nil {
		return AuthorizedProjectSnapshot{}, err
	}
	return AuthorizedProjectSnapshot{Descriptor: d, Context: contextSnapshot, Skills: skillSnapshot}, nil
}

// SnapshotAuthorizedProjectSkills resolves the exact project authority, opens a
// read-only workspace capability, snapshots Skills, and closes the capability
// before returning it to downstream prompt/tool consumers.
func SnapshotAuthorizedProjectSkills(ctx context.Context, resolve ProjectResolverFunc, opener home.RootOpener, projectID, userID, agentID string) (snapshot *skill.ProjectSnapshot, descriptor ProjectDescriptor, resultErr error) {
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
	if scope, name, err := home.ResolveLogicalCoordinate(home.RootAgentWorkspace, d.Path, true); err != nil || scope != home.RootAgentWorkspace || name != d.Path {
		return nil, ProjectDescriptor{}, errors.New("project descriptor path is invalid")
	}
	root, err := opener.OpenRoot(ctx, home.WorkspaceRequest{UserID: userID, AgentID: agentID}, home.RootAgentWorkspace, home.RootReadOnly)
	if err != nil {
		return nil, ProjectDescriptor{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	snapshot, err = skill.SnapshotProjectSkills(ctx, root, d.Path)
	if err != nil {
		return nil, ProjectDescriptor{}, err
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
