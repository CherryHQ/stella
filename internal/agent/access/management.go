package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CherryHQ/stella/internal/agent/providercred"
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
	pep       *Service
	agents    AgentWriter
	assign    AssignmentWriter
	reloader  AgentReloader
	users     UserDirectory
	activity  ActivityReader
	creds     CredentialWriter
	providers ProviderReader
	deletion  OwnerDeletion
	log       *slog.Logger
}

// OwnerDeletion is the destructive Agent lifecycle boundary. A nil dependency
// keeps construction source-compatible but makes Delete fail closed.
type OwnerDeletion interface {
	DeleteAgent(context.Context, string, string) error
}

// ManagementOption configures optional Management dependencies.
type ManagementOption func(*Management)

// WithOwnerDeletion supplies the destructive Home lifecycle for Agent deletion.
func WithOwnerDeletion(d OwnerDeletion) ManagementOption {
	return func(m *Management) { m.deletion = d }
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

// CredentialWriter is the Agent Provider credential encryption/persistence
// boundary. *providercred.Service satisfies it. Management authorizes and
// validates before delegating; it never sees plaintext after handing off.
type CredentialWriter interface {
	List(ctx context.Context, agentID string) ([]providercred.Metadata, error)
	Set(ctx context.Context, agentID string, input providercred.Input) (providercred.Metadata, error)
	Delete(ctx context.Context, agentID, providerID string) error
	CreateAgentWithCredentials(ctx context.Context, agent config.Agent, inputs []providercred.Input) error
}

// ProviderReader lists only canonical Provider IDs so Management can reject an
// alias or unconfigured Provider before encryption without reading global
// Provider config or its API key.
type ProviderReader interface {
	ListProviderIDs(ctx context.Context) ([]string, error)
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
	// ErrInUse reports that a durable resource still references the Agent.
	ErrInUse = errors.New("agent is still in use")
	// ErrUnknownProvider reports a credential input whose provider_id is not an
	// existing canonical Provider (an alias or an unconfigured ID). It is a
	// validation error (400), raised before any encryption.
	ErrUnknownProvider = errors.New("provider credential targets an unknown or non-canonical provider")
	// ErrCredentialsUnavailable reports that credential support is not wired.
	ErrCredentialsUnavailable = errors.New("agent provider credentials are unavailable")
)

// NewManagement builds the Agent management service over the Agent PEP and its
// write/reload/lookup ports. log defaults to slog.Default() when nil.
func NewManagement(pep *Service, agents AgentWriter, assign AssignmentWriter, reloader AgentReloader, users UserDirectory, activity ActivityReader, creds CredentialWriter, providers ProviderReader, log *slog.Logger, opts ...ManagementOption) *Management {
	if log == nil {
		log = slog.Default()
	}
	m := &Management{pep: pep, agents: agents, assign: assign, reloader: reloader, users: users, activity: activity, creds: creds, providers: providers, log: log}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Create authorizes and persists a new agent, enforcing the scope rules,
// generating a unique ID, and — for a restricted agent a non-admin creates —
// auto-assigning the creator. The create+auto-assign pair is atomic from the
// caller's view: a failed assignment compensates by deleting the just-created
// agent, so a restricted agent is never left invisible to its creator. The
// caller supplies a fully transport-validated candidate whose ID is the base
// slug; Management owns the scope decision, uniqueness, workspace, and creator.
func (m *Management) Create(ctx context.Context, authority authz.Authority, candidate config.Agent) (config.Agent, error) {
	candidate, isAdmin, err := m.prepareCreate(ctx, authority, candidate)
	if err != nil {
		return config.Agent{}, err
	}
	if err := m.agents.CreateAgent(ctx, candidate); err != nil {
		return config.Agent{}, fmt.Errorf("%w: create agent: %w", ErrUnavailable, err)
	}
	return m.finishCreate(ctx, isAdmin, candidate)
}

// CreateWithProviderCredentials creates an Agent together with per-Provider key
// overrides in one atomic persist. It shares Create's authorization, scope, and
// auto-assign compensation, adding canonical-Provider validation and encryption
// before the composite write. An empty inputs slice is equivalent to Create.
func (m *Management) CreateWithProviderCredentials(ctx context.Context, authority authz.Authority, candidate config.Agent, inputs []providercred.Input) (config.Agent, error) {
	if len(inputs) == 0 {
		return m.Create(ctx, authority, candidate)
	}
	if m.creds == nil {
		return config.Agent{}, ErrCredentialsUnavailable
	}
	candidate, isAdmin, err := m.prepareCreate(ctx, authority, candidate)
	if err != nil {
		return config.Agent{}, err
	}
	if err := m.validateCredentialProviderIDs(ctx, inputs); err != nil {
		return config.Agent{}, err
	}
	// The credential service encrypts every key, then inserts the Agent and all
	// credential rows in one transaction: a failure at any step persists nothing.
	if err := m.creds.CreateAgentWithCredentials(ctx, candidate, inputs); err != nil {
		return config.Agent{}, mapCredentialError(err)
	}
	return m.finishCreate(ctx, isAdmin, candidate)
}

// prepareCreate authorizes the create and resolves the server-owned fields (scope,
// workspace, creator, unique ID). It performs no persistence.
func (m *Management) prepareCreate(ctx context.Context, authority authz.Authority, candidate config.Agent) (config.Agent, bool, error) {
	acc, err := m.pep.Begin(ctx, authority)
	if err != nil {
		return config.Agent{}, false, err
	}
	if err := acc.CanCreate(); err != nil {
		return config.Agent{}, false, err
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
			return config.Agent{}, false, ErrInvalidScope
		}
	}

	// Workspace is always the default path — never caller-supplied.
	candidate.Workspace = ""
	candidate.CreatorID = string(authority.UserID())
	candidate.ID = m.uniqueAgentID(ctx, candidate.ID)
	return candidate, isAdmin, nil
}

// finishCreate runs the ordinary-creator auto-assign (with compensating delete on
// failure) and the best-effort reload after the Agent — and any credentials — are
// durably persisted. The compensating DeleteAgent cascades to credential rows.
func (m *Management) finishCreate(ctx context.Context, isAdmin bool, candidate config.Agent) (config.Agent, error) {
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
	if m.deletion == nil {
		return fmt.Errorf("%w: delete agent lifecycle is not wired", ErrUnavailable)
	}
	if err := m.deletion.DeleteAgent(ctx, agentID, string(authority.UserID())); err != nil {
		if errors.Is(err, config.ErrAgentInUse) || isAgentInUse(err) {
			return ErrInUse
		}
		return fmt.Errorf("%w: delete agent: %w", ErrUnavailable, err)
	}
	return nil
}

func isAgentInUse(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "23001" || pgErr.Code == "23503") && pgErr.ConstraintName == "webhook_agent_id_fkey"
}

// ListProviderCredentials returns secret-free credential metadata for an Agent.
// Read access is sufficient; only Set/Delete require Manage.
func (m *Management) ListProviderCredentials(ctx context.Context, authority authz.Authority, agentID string) ([]providercred.Metadata, error) {
	if m.creds == nil {
		return nil, ErrCredentialsUnavailable
	}
	acc, err := m.pep.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	if _, err := acc.Read(ctx, agentID); err != nil {
		return nil, err
	}
	metas, err := m.creds.List(ctx, agentID)
	if err != nil {
		return nil, mapCredentialError(err)
	}
	return metas, nil
}

// SetProviderCredential upserts (or rotates) one Agent Provider key override. It
// requires Manage, validates the target is a canonical configured Provider before
// any encryption, and reloads only the affected Agent's runners after commit.
func (m *Management) SetProviderCredential(ctx context.Context, authority authz.Authority, agentID string, input providercred.Input) (providercred.Metadata, error) {
	if m.creds == nil {
		return providercred.Metadata{}, ErrCredentialsUnavailable
	}
	acc, err := m.pep.Begin(ctx, authority)
	if err != nil {
		return providercred.Metadata{}, err
	}
	if _, err := acc.Manage(ctx, agentID); err != nil {
		return providercred.Metadata{}, err
	}
	if err := m.validateProviderIDs(ctx, input.ProviderID); err != nil {
		return providercred.Metadata{}, err
	}
	meta, err := m.creds.Set(ctx, agentID, input)
	if err != nil {
		return providercred.Metadata{}, mapCredentialError(err)
	}
	m.reload(ctx, agentID)
	return meta, nil
}

// DeleteProviderCredential removes an Agent Provider key override, restoring the
// global-key fallback. It requires Manage, is idempotent, and reloads only the
// affected Agent's runners after commit.
func (m *Management) DeleteProviderCredential(ctx context.Context, authority authz.Authority, agentID, providerID string) error {
	if m.creds == nil {
		return ErrCredentialsUnavailable
	}
	acc, err := m.pep.Begin(ctx, authority)
	if err != nil {
		return err
	}
	if _, err := acc.Manage(ctx, agentID); err != nil {
		return err
	}
	if err := m.validateProviderIDs(ctx, providerID); err != nil {
		return err
	}
	if err := m.creds.Delete(ctx, agentID, providerID); err != nil {
		return mapCredentialError(err)
	}
	m.reload(ctx, agentID)
	return nil
}

func (m *Management) validateCredentialProviderIDs(ctx context.Context, inputs []providercred.Input) error {
	ids := make([]string, len(inputs))
	for i, input := range inputs {
		ids[i] = input.ProviderID
	}
	return m.validateProviderIDs(ctx, ids...)
}

// validateProviderIDs rejects IDs that are not existing canonical Providers. A
// type alias and an unconfigured ID both fail here, before secret work.
func (m *Management) validateProviderIDs(ctx context.Context, providerIDs ...string) error {
	if m.providers == nil {
		return ErrCredentialsUnavailable
	}
	ids, err := m.providers.ListProviderIDs(ctx)
	if err != nil {
		return fmt.Errorf("%w: list providers: %w", ErrUnavailable, err)
	}
	canonical := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		canonical[id] = struct{}{}
	}
	for _, providerID := range providerIDs {
		if _, ok := canonical[providerID]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownProvider, providerID)
		}
	}
	return nil
}

// mapCredentialError surfaces providercred validation sentinels unchanged (they
// map to 400/503 at the transport) and wraps anything else as unavailable.
func mapCredentialError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, providercred.ErrEmptyProviderID),
		errors.Is(err, providercred.ErrEmptyAPIKey),
		errors.Is(err, providercred.ErrDuplicateProvider),
		errors.Is(err, providercred.ErrTooManyCredentials),
		errors.Is(err, providercred.ErrProviderIDTooLong),
		errors.Is(err, providercred.ErrAPIKeyTooLong):
		return err
	case errors.Is(err, providercred.ErrUnavailable):
		return fmt.Errorf("%w: %w", ErrCredentialsUnavailable, err)
	default:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
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
