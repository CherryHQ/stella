package access

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/jackc/pgx/v5"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// ArtifactReadInput identifies one shareable file through semantic workspace
// coordinates. It never accepts or returns a host/provider coordinate.
type ArtifactReadInput struct {
	AgentID   string
	SessionID string
	Scope     WorkspaceScope
	Path      string
	MaxBytes  int64
}

// ArtifactReadResult is an exact, bounded snapshot read from the selected
// session runtime. Info is the durable session identity that authorized it.
type ArtifactReadResult struct {
	Info    agentsession.Info
	Path    string
	Name    string
	Content []byte
}

// ReadArtifact reads one artifact from exactly one user-owned session. It is
// intentionally separate from Workspace: share publication permits a delegated
// AgentActor bound to this agent, while public workspace APIs remain user-only.
func (a *Access) ReadArtifact(ctx context.Context, in ArtifactReadInput) (ArtifactReadResult, error) {
	if in.AgentID == "" || in.SessionID == "" || in.MaxBytes < 0 {
		return ArtifactReadResult{}, ErrInvalid
	}
	// Parse semantic aliases and reject traversal/host coordinates before any
	// runtime lease. The returned name is the provider's canonical mount path.
	scope, _, name, err := workspacePath(in.Scope, in.Path, false)
	if err != nil {
		return ArtifactReadResult{}, err
	}
	info, err := a.artifactSession(ctx, in.AgentID, in.SessionID)
	if err != nil {
		return ArtifactReadResult{}, err
	}
	runtime, err := a.svc.runtimeFor(info.AgentID)
	if err != nil {
		return ArtifactReadResult{}, err
	}
	filesystemRuntime, ok := runtime.(FilesystemRuntimeService)
	if !ok {
		return ArtifactReadResult{}, fmt.Errorf("%w: runtime lacks filesystem capability", ErrUnavailable)
	}
	var result ArtifactReadResult
	err = filesystemRuntime.UseFilesystem(ctx, info, func(filesystem pkgsandbox.Filesystem) error {
		data, _, err := readFilesystemRaw(ctx, filesystem, name, in.MaxBytes)
		if err != nil {
			return err
		}
		result = ArtifactReadResult{Info: info, Path: workspaceRelativePath(scope, name), Name: path.Base(name), Content: data}
		return nil
	})
	if err != nil {
		return ArtifactReadResult{}, err
	}
	return result, nil
}

// artifactSession resolves only the durable facts needed for the private share
// policy. It deliberately does not call Workspace or the Agent PEP: a user may
// publish from their own session regardless of public workspace policy, and a
// delegated agent may publish only from its exact bound agent/session.
func (a *Access) artifactSession(ctx context.Context, agentID, sessionID string) (agentsession.Info, error) {
	if a.authority.Kind() != authz.ActorUser && a.authority.Kind() != authz.ActorAgent {
		return agentsession.Info{}, ErrForbidden
	}
	owner := string(a.authority.UserID())
	if owner == "" {
		return agentsession.Info{}, ErrForbidden
	}
	conv, err := a.svc.q.GetConversationForSessionAccess(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agentsession.Info{}, ErrNotFound
		}
		return agentsession.Info{}, fmt.Errorf("%w: get artifact session facts: %w", ErrUnavailable, err)
	}
	if !conv.UserID.Valid || !conv.AgentID.Valid || conv.UserID.String != owner || conv.AgentID.String != agentID {
		return agentsession.Info{}, ErrForbidden
	}
	loadCtx := authz.WithAgentID(authz.WithUserID(ctx, conv.UserID.String), conv.AgentID.String)
	record, err := a.svc.memory.LoadInfo(loadCtx, sessionID)
	if err != nil {
		return agentsession.Info{}, ErrNotFound
	}
	info, err := agentsession.InfoFromRecord(record)
	if err != nil {
		return agentsession.Info{}, fmt.Errorf("%w: invalid artifact session record: %w", ErrUnavailable, err)
	}
	if info.UserID != owner || info.AgentID != agentID || info.GroupID != "" {
		return agentsession.Info{}, ErrForbidden
	}
	if a.authority.Kind() == authz.ActorAgent && string(a.authority.AgentID()) != agentID {
		return agentsession.Info{}, ErrForbidden
	}
	return info, nil
}
