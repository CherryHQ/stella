package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
)

// Management owns the Agent write use cases: create/update/delete, admin user
// assignment, and the conversation-activity read model. Every method authorizes
// through the Agent PEP (Service) before any durable read or write, then performs
// the persistence, cross-write compensation, and best-effort runtime reload the
// HTTP transport used to orchestrate itself. Splitting these off the read-only
// Service keeps the PEP's decision surface narrow while giving the Agent domain a
// single owner for its mutating invariants (unique ID, creator auto-assignment
// atomicity, scope rules).
type Management struct {
	pep      *Service
	agents   AgentWriter
	assign   AssignmentWriter
	reloader AgentReloader
	users    UserDirectory
	activity ActivityReader
	log      *slog.Logger
}

// AgentWriter persists agent rows. config.Store satisfies it; the interface stays
// narrow so Management cannot reach unrelated aggregate config.
type AgentWriter interface {
	GetAgent(ctx context.Context, id string) (config.Agent, error)
	CreateAgent(ctx context.Context, a config.Agent) error
	UpdateAgent(ctx context.Context, a config.Agent) error
	DeleteAgent(ctx context.Context, id string) error
}

// AssignmentWriter mutates and lists the user<->agent assignment relation.
// auth.AuthStore satisfies it.
type AssignmentWriter interface {
	AssignAgent(ctx context.Context, userID, agentID string) error
	RemoveAgent(ctx context.Context, userID, agentID string) error
	ListAgentUserIDs(ctx context.Context, agentID string) ([]string, error)
}

// AgentReloader refreshes an agent's runtime state after a durable change.
// agent.PoolManager.SyncAgent satisfies it. Reload is eventually-consistent, so
// Management treats a failure as best-effort (see Create/Update/Delete).
type AgentReloader interface {
	SyncAgent(ctx context.Context, agentID string) error
}

// UserRef is the account display data an assignment view needs. It is a domain
// value, not an account row, so Management never depends on the Account boundary's
// internal shapes.
type UserRef struct {
	ID    string
	Email string
}

// UserDirectory resolves assignment-target account display data. The composition
// root backs it with the account user store; LookupUsers skips IDs that no longer
// resolve so a stale assignment link never breaks the admin listing.
type UserDirectory interface {
	LookupUser(ctx context.Context, userID string) (UserRef, error)
	LookupUsers(ctx context.Context, userIDs []string) ([]UserRef, error)
}

// ActivityReader is the conversation-activity read model: the last-active time per
// agent for one user's own conversations.
type ActivityReader interface {
	ListAgentLastActive(ctx context.Context, userID string) (map[string]time.Time, error)
}

// Management-specific typed errors. Authorization/availability reuse the Service
// sentinels (ErrForbidden / ErrNotFound / ErrUnavailable) so the transport maps
// every Agent error through one vocabulary.
var (
	// ErrInvalidScope reports an admin-supplied scope that is neither system nor
	// restricted. It is a validation error (400), never an authorization denial.
	ErrInvalidScope = errors.New("agent scope must be 'system' or 'restricted'")
	// ErrUserNotFound reports an assignment target that does not exist.
	ErrUserNotFound = errors.New("assignment target user not found")
)

// NewManagement builds the Agent management service over the Agent PEP and its
// write/reload/lookup ports. log defaults to slog.Default() when nil.
func NewManagement(pep *Service, agents AgentWriter, assign AssignmentWriter, reloader AgentReloader, users UserDirectory, activity ActivityReader, log *slog.Logger) *Management {
	if log == nil {
		log = slog.Default()
	}
	return &Management{pep: pep, agents: agents, assign: assign, reloader: reloader, users: users, activity: activity, log: log}
}

// Create authorizes and persists a new agent, enforcing the scope rules,
// generating a unique ID, and — for a restricted agent a non-admin creates —
// auto-assigning the creator. The create+auto-assign pair is atomic from the
// caller's view: a failed assignment compensates by deleting the just-created
// agent, so a restricted agent is never left invisible to its creator. The
// caller supplies a fully transport-validated candidate whose ID is the base
// slug; Management owns the scope decision, uniqueness, workspace, and creator.
func (m *Management) Create(ctx context.Context, authority authz.Authority, candidate config.Agent) (config.Agent, error) {
	acc, err := m.pep.Begin(ctx, authority)
	if err != nil {
		return config.Agent{}, err
	}
	if err := acc.CanCreate(); err != nil {
		return config.Agent{}, err
	}

	isAdmin := authority.IsAdmin()
	if !isAdmin {
		// Non-admins always get restricted scope, auto-assigned below.
		candidate.Scope = config.AgentScopeRestricted
	} else {
		if candidate.Scope == "" {
			candidate.Scope = config.AgentScopeSystem
		}
		if candidate.Scope != config.AgentScopeSystem && candidate.Scope != config.AgentScopeRestricted {
			return config.Agent{}, ErrInvalidScope
		}
	}

	// Workspace is always the default path — never caller-supplied.
	candidate.Workspace = ""
	candidate.CreatorID = string(authority.UserID())
	candidate.ID = m.uniqueAgentID(ctx, candidate.ID)

	if err := m.agents.CreateAgent(ctx, candidate); err != nil {
		return config.Agent{}, fmt.Errorf("%w: create agent: %w", ErrUnavailable, err)
	}

	if !isAdmin && candidate.Scope == config.AgentScopeRestricted {
		if err := m.assign.AssignAgent(ctx, candidate.CreatorID, candidate.ID); err != nil {
			// Compensate: without the assignment the creator cannot see their own
			// restricted agent, so roll the create back rather than report a false
			// success. A failed compensation is logged; that residual orphan is the
			// documented ceiling of compensation without a shared transaction.
			if delErr := m.agents.DeleteAgent(ctx, candidate.ID); delErr != nil {
				m.log.Error("compensating delete after failed auto-assign", "agent_id", candidate.ID, "error", delErr)
			}
			return config.Agent{}, fmt.Errorf("%w: auto-assign creator: %w", ErrUnavailable, err)
		}
	}

	// Reload is eventually-consistent: the agent is durably persisted, so a failed
	// sync is logged and re-applied on the next lifecycle event, never failing the
	// create.
	m.reload(ctx, candidate.ID)
	return candidate, nil
}

// Update authorizes (Manage) and persists an agent edit. Non-admins cannot change
// scope; admins default an empty scope to system and reject any other value. The
// caller supplies a transport-validated candidate; reload is best-effort.
func (m *Management) Update(ctx context.Context, authority authz.Authority, candidate config.Agent) (config.Agent, error) {
	acc, err := m.pep.Begin(ctx, authority)
	if err != nil {
		return config.Agent{}, err
	}
	existing, err := acc.Manage(ctx, candidate.ID)
	if err != nil {
		return config.Agent{}, err
	}

	if !authority.IsAdmin() {
		candidate.Scope = existing.Scope
	} else {
		if candidate.Scope == "" {
			candidate.Scope = config.AgentScopeSystem
		}
		if candidate.Scope != config.AgentScopeSystem && candidate.Scope != config.AgentScopeRestricted {
			return config.Agent{}, ErrInvalidScope
		}
	}

	// Workspace and creator are durable server-owned facts, never transport
	// inputs. Keep workspace on the canonical default path and preserve the
	// persisted creator in both storage and the returned domain value.
	candidate.Workspace = ""
	candidate.CreatorID = existing.CreatorID

	if err := m.agents.UpdateAgent(ctx, candidate); err != nil {
		return config.Agent{}, fmt.Errorf("%w: update agent: %w", ErrUnavailable, err)
	}
	m.reload(ctx, candidate.ID)
	return candidate, nil
}

// Delete authorizes (Delete) and removes an agent. Reload is best-effort.
func (m *Management) Delete(ctx context.Context, authority authz.Authority, agentID string) error {
	acc, err := m.pep.Begin(ctx, authority)
	if err != nil {
		return err
	}
	if _, err := acc.Delete(ctx, agentID); err != nil {
		return err
	}
	if err := m.agents.DeleteAgent(ctx, agentID); err != nil {
		return fmt.Errorf("%w: delete agent: %w", ErrUnavailable, err)
	}
	m.reload(ctx, agentID)
	return nil
}

// ListAssignedUsers returns the account references for the users assigned to an
// agent. Admin-only: the gate runs before any durable read.
func (m *Management) ListAssignedUsers(ctx context.Context, authority authz.Authority, agentID string) ([]UserRef, error) {
	if err := requireAdmin(authority); err != nil {
		return nil, err
	}
	ids, err := m.assign.ListAgentUserIDs(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("%w: list agent users: %w", ErrUnavailable, err)
	}
	refs, err := m.users.LookupUsers(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: lookup users: %w", ErrUnavailable, err)
	}
	return refs, nil
}

// AssignUser assigns a user to an agent. Admin-only. It verifies the agent then
// the user exists (preserving the historical 404 order) before writing.
func (m *Management) AssignUser(ctx context.Context, authority authz.Authority, agentID, userID string) (UserRef, error) {
	if err := requireAdmin(authority); err != nil {
		return UserRef{}, err
	}
	if _, err := m.agents.GetAgent(ctx, agentID); err != nil {
		return UserRef{}, ErrNotFound
	}
	ref, err := m.users.LookupUser(ctx, userID)
	if err != nil {
		return UserRef{}, ErrUserNotFound
	}
	if err := m.assign.AssignAgent(ctx, userID, agentID); err != nil {
		return UserRef{}, fmt.Errorf("%w: assign agent: %w", ErrUnavailable, err)
	}
	return ref, nil
}

// RemoveUser unassigns a user from an agent. Admin-only.
func (m *Management) RemoveUser(ctx context.Context, authority authz.Authority, agentID, userID string) error {
	if err := requireAdmin(authority); err != nil {
		return err
	}
	if err := m.assign.RemoveAgent(ctx, userID, agentID); err != nil {
		return fmt.Errorf("%w: remove agent: %w", ErrUnavailable, err)
	}
	return nil
}

// ListAgentLastActive returns the last-active time per agent for the given user's
// own conversations. It carries no additional authorization: the caller has
// already been list-authorized for the agents it enriches, and the read is scoped
// to that same user's activity.
func (m *Management) ListAgentLastActive(ctx context.Context, userID string) (map[string]time.Time, error) {
	if m.activity == nil {
		return map[string]time.Time{}, nil
	}
	out, err := m.activity.ListAgentLastActive(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("%w: list agent activity: %w", ErrUnavailable, err)
	}
	return out, nil
}

// uniqueAgentID returns base when free, otherwise the first base-N suffix whose
// lookup fails. It preserves the legacy dedup: any GetAgent error means the ID is
// available, so a transient store error can pick a fresh ID rather than collide.
func (m *Management) uniqueAgentID(ctx context.Context, base string) string {
	if _, err := m.agents.GetAgent(ctx, base); err != nil {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, err := m.agents.GetAgent(ctx, candidate); err != nil {
			return candidate
		}
	}
}

func (m *Management) reload(ctx context.Context, agentID string) {
	if m.reloader == nil {
		return
	}
	if err := m.reloader.SyncAgent(ctx, agentID); err != nil {
		m.log.Error("sync agent pool", "agent_id", agentID, "error", err)
	}
}

// requireAdmin gates the admin-only assignment use cases. It fails closed on an
// invalid or non-admin authority, before any durable read.
func requireAdmin(authority authz.Authority) error {
	if !authority.Valid() || !authority.IsAdmin() {
		return ErrForbidden
	}
	return nil
}
