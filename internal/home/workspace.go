package home

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// WorkspaceRequest selects exactly one principal. Group wins because channel
// sessions carry a synthetic user ID as well.
type WorkspaceRequest struct {
	UserID  string
	GroupID string
	AgentID string
}

// WorkspaceView is the Phase-1 bridge from durable Home identity to the local
// compatibility layout. Paths are deliberately internal-only and disappear
// when the Phase-2 filesystem boundary consumes attachments directly.
type WorkspaceView struct {
	Principal            sandbox.HomeAttachment
	Agent                sandbox.HomeAttachment
	SystemSkillRoot      sandbox.HomeAttachment
	SystemAgentSkillRoot sandbox.HomeAttachment
	PrincipalRoot        string
	DataRoot             string
	AgentRoot            string
}

// WorkspaceViewer is the single runner/prompt dependency on persistent Homes.
type WorkspaceViewer interface {
	WorkspaceView(context.Context, WorkspaceRequest) (WorkspaceView, error)
}

// localWorkspaceProjector is deliberately narrower than Store: only adapters
// that can safely materialize and project the legacy local layout implement it.
// It is not a generic host-path API.
type localWorkspaceProjector interface {
	ProjectWorkspace(Record, Record) (principalRoot, dataRoot, agentRoot string, err error)
}

// WorkspaceView resolves fresh registry rows before projecting local paths.
// A caller cannot supply an attachment here, so every attachment in the view
// is DB-backed and ready at the instant the view is captured.
func (r *Registry) WorkspaceView(ctx context.Context, req WorkspaceRequest) (WorkspaceView, error) {
	if req.AgentID == "" {
		return WorkspaceView{}, fmt.Errorf("home: workspace agent ID is required")
	}
	view := WorkspaceView{}
	var err error
	system, err := r.Ensure(ctx, SystemSkills())
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: ensure system Skill root: %w", err)
	}
	view.SystemSkillRoot, err = r.resolveRecord(ctx, system, true)
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: resolve system Skill root: %w", err)
	}
	systemAgent, err := r.Ensure(ctx, SystemAgentSkills(req.AgentID))
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: ensure system Agent Skill root: %w", err)
	}
	view.SystemAgentSkillRoot, err = r.resolveRecord(ctx, systemAgent, true)
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: resolve system Agent Skill root: %w", err)
	}

	key, ok := workspacePrincipal(req)
	if !ok {
		return view, nil
	}
	principal, err := r.Ensure(ctx, key)
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: ensure principal workspace: %w", err)
	}
	agent, err := r.Ensure(ctx, Agent(key.PrincipalKind, key.PrincipalID, req.AgentID))
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: ensure Agent workspace: %w", err)
	}
	view.Principal, err = r.resolveRecord(ctx, principal, false)
	if err != nil {
		return WorkspaceView{}, err
	}
	view.Agent, err = r.resolveRecord(ctx, agent, false)
	if err != nil {
		return WorkspaceView{}, err
	}
	store, ok := r.stores[principal.StoreID].(localWorkspaceProjector)
	if !ok || agent.StoreID != principal.StoreID {
		return WorkspaceView{}, fmt.Errorf("home: Store %q has no local workspace projection", principal.StoreID)
	}
	view.PrincipalRoot, view.DataRoot, view.AgentRoot, err = store.ProjectWorkspace(principal, agent)
	if err != nil {
		return WorkspaceView{}, err
	}
	return view, nil
}

func workspacePrincipal(req WorkspaceRequest) (Key, bool) {
	if req.GroupID != "" {
		return Principal(GroupPrincipal, req.GroupID), true
	}
	if req.UserID != "" {
		return Principal(UserPrincipal, req.UserID), true
	}
	return Key{}, false
}
