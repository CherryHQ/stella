package home

import (
	"context"
	"fmt"
	"sort"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
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
	PrepareWorkspace(Record, Record) error
	WorkspacePaths(Record, Record) (principalRoot, dataRoot, agentRoot string, err error)
}

// WorkspaceView projects compatibility paths outside a DB transaction, then
// captures ready attachments behind the owner advisory gate. The bounded local
// owner lock is a Phase-1 one-replica ceiling: it keeps deletion from purging a
// Home while a local Store call is in flight; Phase 3 uses SessionSandbox fencing.
func (r *Registry) WorkspaceView(ctx context.Context, req WorkspaceRequest) (WorkspaceView, error) {
	if req.AgentID == "" {
		return WorkspaceView{}, fmt.Errorf("home: workspace agent ID is required")
	}
	principalKey, hasPrincipal := workspacePrincipal(req)
	ownerKeys := workspaceOwnerKeys(req.AgentID, principalKey, hasPrincipal)
	unlock, err := r.lockOwnerKeys(ctx, ownerKeys)
	if err != nil {
		return WorkspaceView{}, err
	}
	defer unlock()

	// Store Ensure and local compatibility preparation perform filesystem I/O and
	// must not retain a database connection or advisory transaction lock.
	system, err := r.Ensure(ctx, SystemSkills())
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: ensure system Skill root: %w", err)
	}
	systemAgent, err := r.Ensure(ctx, SystemAgentSkills(req.AgentID))
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: ensure system Agent Skill root: %w", err)
	}
	var principal, agent Record
	var projector localWorkspaceProjector
	var principalRoot, dataRoot, agentRoot string
	if hasPrincipal {
		principal, err = r.Ensure(ctx, principalKey)
		if err != nil {
			return WorkspaceView{}, fmt.Errorf("home: ensure principal workspace: %w", err)
		}
		agent, err = r.Ensure(ctx, Agent(principalKey.PrincipalKind, principalKey.PrincipalID, req.AgentID))
		if err != nil {
			return WorkspaceView{}, fmt.Errorf("home: ensure Agent workspace: %w", err)
		}
		var ok bool
		projector, ok = r.stores[principal.StoreID].(localWorkspaceProjector)
		if !ok || agent.StoreID != principal.StoreID {
			return WorkspaceView{}, fmt.Errorf("home: Store %q has no local workspace projection", principal.StoreID)
		}
		if err := projector.PrepareWorkspace(principal, agent); err != nil {
			return WorkspaceView{}, err
		}
		principalRoot, dataRoot, agentRoot, err = projector.WorkspacePaths(principal, agent)
		if err != nil {
			return WorkspaceView{}, err
		}
	}

	ready := []Record{system, systemAgent}
	if hasPrincipal {
		ready = append(ready, principal, agent)
	}
	pins := make([]readyRootPin, 0, len(ready))
	defer func() {
		for _, pin := range pins {
			_ = pin.Close()
		}
	}()
	for _, record := range ready {
		pin, err := r.pinReadyRoot(record)
		if err != nil {
			return WorkspaceView{}, fmt.Errorf("home: pin ready workspace storage: %w", err)
		}
		pins = append(pins, pin)
	}

	// The final transaction is DB-only. It serializes with owner deletion and
	// revalidates every record captured above before any attachment is returned.
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: begin workspace owner gate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	locks := append([]string(nil), ownerKeys...)
	sort.Strings(locks)
	for _, lock := range locks {
		if err := q.LockStorageHomeOwner(ctx, lock); err != nil {
			return WorkspaceView{}, fmt.Errorf("home: lock workspace owner gate: %w", err)
		}
	}
	view := WorkspaceView{}
	view.SystemSkillRoot, err = r.resolveRecordWithQueries(ctx, q, system, true)
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: resolve system Skill root: %w", err)
	}
	view.SystemAgentSkillRoot, err = r.resolveRecordWithQueries(ctx, q, systemAgent, true)
	if err != nil {
		return WorkspaceView{}, fmt.Errorf("home: resolve system Agent Skill root: %w", err)
	}
	if hasPrincipal {
		view.Principal, err = r.resolveRecordWithQueries(ctx, q, principal, false)
		if err != nil {
			return WorkspaceView{}, err
		}
		view.Agent, err = r.resolveRecordWithQueries(ctx, q, agent, false)
		if err != nil {
			return WorkspaceView{}, err
		}
		if agent.StoreID != principal.StoreID {
			return WorkspaceView{}, fmt.Errorf("home: Store %q changed during workspace projection", principal.StoreID)
		}
		view.PrincipalRoot, view.DataRoot, view.AgentRoot = principalRoot, dataRoot, agentRoot
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkspaceView{}, fmt.Errorf("home: commit workspace view: %w", err)
	}
	// Inspect once more after DB revalidation. This catches an exact-root
	// replacement during the DB-only gate without moving filesystem I/O into the
	// transaction; the process owner lock remains held through this check.
	for _, pin := range pins {
		if err := pin.Revalidate(); err != nil {
			return WorkspaceView{}, fmt.Errorf("home: ready storage changed during workspace revalidation: %w", err)
		}
	}
	return view, nil
}

func workspaceOwnerKeys(agentID string, principal Key, hasPrincipal bool) []string {
	keys := []string{ownerLockKey(OwnerAgent, agentID)}
	if !hasPrincipal {
		return keys
	}
	kind := OwnerUser
	if principal.PrincipalKind == GroupPrincipal {
		kind = OwnerGroup
	}
	return append(keys, ownerLockKey(kind, principal.PrincipalID))
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
