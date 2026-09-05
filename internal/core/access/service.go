// Package access is the policy-enforcement point for Agent resources.
package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/platform/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

var (
	ErrForbidden   = errors.New("agent access forbidden")
	ErrNotFound    = errors.New("agent not found")
	ErrUnavailable = errors.New("agent authorization unavailable")
)

// AgentStore is deliberately narrow, but includes list because collection
// filtering belongs at the PEP rather than in transports or SQL.
type AgentStore interface {
	GetAgent(ctx context.Context, id string) (config.Agent, error)
	ListAgents(ctx context.Context) ([]config.Agent, error)
}

// AgentSnapshotStore supplies coherent Agent Settings values and conditional
// versions. It is deliberately optional for the general PEP, but model-facing
// management reads fail closed unless this store capability is wired.
type AgentSnapshotStore interface {
	GetAgentSnapshot(ctx context.Context, id string) (config.AgentSnapshot, error)
	ListAgentSnapshots(ctx context.Context) ([]config.AgentSnapshot, error)
}

type AssignmentStore interface {
	ListUserAgentIDs(ctx context.Context, userID string) ([]string, error)
}

// dedicatedChannelStore is intentionally asserted at the dedicated-use boundary:
// a held channel binding alone is not authority for an arbitrary agent.
type dedicatedChannelStore interface {
	GetChannel(ctx context.Context, id string) (config.Channel, error)
}

type Service struct {
	agents      AgentStore
	assign      AssignmentStore
	guestPolicy pkgchannel.GuestPolicyResolver
}

type Option func(*Service)

func WithGuestPolicyDecoder(decoder pkgchannel.GuestPolicyResolver) Option {
	return func(s *Service) { s.guestPolicy = decoder }
}

// SetGuestPolicyDecoder binds the host registration lookup after plugin
// catalog construction and before request handling starts.
func (s *Service) SetGuestPolicyDecoder(decoder pkgchannel.GuestPolicyResolver) {
	s.guestPolicy = decoder
}

func NewService(agents AgentStore, assign AssignmentStore, options ...Option) *Service {
	s := &Service{agents: agents, assign: assign}
	for _, option := range options {
		option(s)
	}
	return s
}

// Access captures one validated authority and caches its assignment relation for
// the duration of a multi-step use case.
type Access struct {
	svc       *Service
	authority authz.Authority
	userID    string
	loaded    bool
	assigned  map[string]bool
	assignErr error
}

func (s *Service) Begin(_ context.Context, authority authz.Authority) (*Access, error) {
	if !authority.Valid() {
		return nil, ErrForbidden
	}
	return &Access{svc: s, authority: authority, userID: string(authority.UserID())}, nil
}

func (a *Access) Read(ctx context.Context, agentID string) (config.Agent, error) {
	return a.decide(ctx, agentID, authz.ActionRead, false)
}

// ReadSnapshot applies the ordinary Agent PEP read decision to a single durable
// snapshot. The policy stays centralized here while Agent tools receive a value
// and CAS version that cannot be mixed across rows or reads.
func (a *Access) ReadSnapshot(ctx context.Context, agentID string) (config.AgentSnapshot, error) {
	snapshot, err := a.loadSnapshot(ctx, agentID)
	if err != nil {
		return config.AgentSnapshot{}, err
	}
	if _, err := a.decideLoaded(ctx, snapshot.Agent, authz.ActionRead, false); err != nil {
		return config.AgentSnapshot{}, err
	}
	return snapshot, nil
}

func (a *Access) Use(ctx context.Context, agentID string) (config.Agent, error) {
	return a.decide(ctx, agentID, authz.ActionExecute, false)
}

func (a *Access) Manage(ctx context.Context, agentID string) (config.Agent, error) {
	return a.decide(ctx, agentID, authz.ActionManage, false)
}

func (a *Access) Delete(ctx context.Context, agentID string) (config.Agent, error) {
	return a.decide(ctx, agentID, authz.ActionDelete, false)
}

// UseDedicated requires both the authority's exact held channel binding and a
// current enabled DB binding from that channel to this exact agent. Either
// mismatch fails closed; channel ID by itself is never an agent-use capability.
func (a *Access) UseDedicated(ctx context.Context, agentID, channelID string) (config.Agent, error) {
	return a.useDedicated(ctx, agentID, channelID, "")
}

// UseDedicatedForType additionally requires the current channel type to match
// the platform that minted the queued chat. This prevents a channel record that
// was reconfigured for another platform from authorizing stale queued work.
func (a *Access) UseDedicatedForType(ctx context.Context, agentID, channelID, channelType string) (config.Agent, error) {
	if channelType == "" {
		return config.Agent{}, ErrForbidden
	}
	return a.useDedicated(ctx, agentID, channelID, channelType)
}

func (a *Access) useDedicated(ctx context.Context, agentID, channelID, channelType string) (config.Agent, error) {
	if channelID == "" || (a.authority.Kind() != authz.ActorUser && a.authority.Kind() != authz.ActorGuest) || a.authority.ChannelBindingID() != channelID {
		return config.Agent{}, ErrForbidden
	}
	channels, ok := a.svc.agents.(dedicatedChannelStore)
	if !ok {
		return config.Agent{}, ErrUnavailable
	}
	channel, err := channels.GetChannel(ctx, channelID)
	if err != nil {
		if isNotFound(err) {
			return config.Agent{}, ErrNotFound
		}
		return config.Agent{}, fmt.Errorf("%w: get channel: %w", ErrUnavailable, err)
	}
	if !channel.Enabled || channel.AgentID != agentID || (channelType != "" && channel.Type != channelType) {
		return config.Agent{}, ErrForbidden
	}
	if a.authority.Kind() == authz.ActorGuest {
		if !a.allowsUnlinkedGuestDM(channel) {
			return config.Agent{}, ErrForbidden
		}
	}
	return a.decide(ctx, agentID, authz.ActionExecute, true)
}

func (a *Access) allowsUnlinkedGuestDM(channel config.Channel) bool {
	if !channel.Enabled {
		return false
	}
	if a.svc.guestPolicy == nil {
		return false
	}
	cfg, err := a.svc.guestPolicy(channel.Type, channel.Config)
	return err == nil && cfg.AllowDM && cfg.AllowUnlinkedDM
}

// AuthorizeViaChannelBinding authorizes read/execute of agentID for a trusted
// dedicated-channel Authority: the exact held channel binding whose CURRENT
// persisted channel→agent binding names this exact agent. It is the Agent PEP's
// sole interpretation of a dedicated channel, so cross-domain callers (the
// session flow's Agent gate) never re-derive channel bindings themselves. A held
// binding alone is never authority for an arbitrary agent; every mismatch fails
// closed.
func (s *Service) AuthorizeViaChannelBinding(ctx context.Context, authority authz.Authority, agentID string) error {
	a, err := s.Begin(ctx, authority)
	if err != nil {
		return err
	}
	binding := authority.ChannelBindingID()
	if binding == "" {
		return ErrForbidden
	}
	switch _, err := a.UseDedicated(ctx, agentID, binding); {
	case err == nil:
		return nil
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound):
		return ErrForbidden
	default:
		return err
	}
}

func (a *Access) CanUse(ctx context.Context, agentID string) (bool, error) {
	_, err := a.Use(ctx, agentID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return false, err
}

func (a *Access) CanList() error {
	if a.authority.IsAdmin() || a.authority.Kind() == authz.ActorUser {
		return nil
	}
	return ErrForbidden
}

func (a *Access) CanCreate() error {
	if a.authority.IsAdmin() || a.authority.Kind() == authz.ActorUser {
		return nil
	}
	return ErrForbidden
}

// ListReadable returns the caller's own fleet: the agents they can actually
// use, plus the ones they created whatever state those are in.
//
// deploymentWide is the admin's separate, explicit ask for everyone's agents.
// Without it an admin gets the same personal fleet as anyone else — being able
// to reach every agent is not a reason to be shown every agent, and the pages
// that browse your own agents should not turn into a directory of everybody's
// on one account. SQL may narrow candidates for performance, but never decides
// visibility.
func (a *Access) ListReadable(ctx context.Context, deploymentWide bool) ([]config.Agent, error) {
	if err := a.CanList(); err != nil {
		return nil, err
	}
	agents, err := a.svc.agents.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: list agents: %w", ErrUnavailable, err)
	}
	out := make([]config.Agent, 0, len(agents))
	for _, agent := range agents {
		ok, err := a.readable(ctx, agent, deploymentWide)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, agent)
		}
	}
	return out, nil
}

// ListReadableSnapshots applies the same PEP collection rule as ListReadable,
// retaining each authorized Agent's version from the exact row that supplied its
// projection. No management adapter repeats the policy or re-reads versions.
func (a *Access) ListReadableSnapshots(ctx context.Context, deploymentWide bool) ([]config.AgentSnapshot, error) {
	if err := a.CanList(); err != nil {
		return nil, err
	}
	store, ok := a.svc.agents.(AgentSnapshotStore)
	if !ok {
		return nil, fmt.Errorf("%w: agent snapshots are not wired", ErrUnavailable)
	}
	snapshots, err := store.ListAgentSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: list agent snapshots: %w", ErrUnavailable, err)
	}
	out := make([]config.AgentSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		ok, err := a.readable(ctx, snapshot.Agent, deploymentWide)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, snapshot)
		}
	}
	return out, nil
}

func (a *Access) readable(ctx context.Context, agent config.Agent, deploymentWide bool) (bool, error) {
	if deploymentWide && a.authority.IsAdmin() {
		return true, nil
	}
	return a.inOwnFleet(ctx, agent)
}

// inOwnFleet is the collection-membership rule, deliberately written without the
// admin superuser reach that decide() applies. Read authority answers "may I
// look at this if I ask for it"; this answers the narrower "is this mine",
// which is what a list of your agents should show.
func (a *Access) inOwnFleet(ctx context.Context, ag config.Agent) (bool, error) {
	// Your own agent stays listed whatever its state. You are the one person who
	// can turn a disabled one back on, and the UI reaches an agent's
	// configuration through this list — dropping it would make "disable" a
	// one-way door out of your own agent.
	if a.isCreator(ag) {
		return true, nil
	}
	if !ag.Enabled {
		return false, nil
	}
	// An agent open to everyone is in everyone's fleet. An unreadable scope is
	// simply not that, and fails closed into the assignment lookup below.
	if scope, err := canonicalScope(ag.Scope); err == nil && scope == config.AgentScopeSystem {
		return true, nil
	}
	if a.authority.Kind() != authz.ActorUser || a.userID == "" {
		return false, nil
	}
	return a.assignedTo(ctx, ag.ID)
}

func (s *Service) Read(ctx context.Context, authority authz.Authority, agentID string) (config.Agent, error) {
	return oneShot(s, ctx, authority, func(a *Access) (config.Agent, error) { return a.Read(ctx, agentID) })
}

func (s *Service) Use(ctx context.Context, authority authz.Authority, agentID string) (config.Agent, error) {
	return oneShot(s, ctx, authority, func(a *Access) (config.Agent, error) { return a.Use(ctx, agentID) })
}

func (s *Service) Manage(ctx context.Context, authority authz.Authority, agentID string) (config.Agent, error) {
	return oneShot(s, ctx, authority, func(a *Access) (config.Agent, error) { return a.Manage(ctx, agentID) })
}

func (s *Service) Delete(ctx context.Context, authority authz.Authority, agentID string) (config.Agent, error) {
	return oneShot(s, ctx, authority, func(a *Access) (config.Agent, error) { return a.Delete(ctx, agentID) })
}

// CanUseAsUser evaluates one ordinary, non-admin user's current execute access.
// It is intended for trusted adapters validating a durable delegation before
// persisting it; callers receive only the decision, never a minted Authority.
func (s *Service) CanUseAsUser(ctx context.Context, userID, agentID string) (bool, error) {
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		return false, ErrForbidden
	}
	access, err := s.Begin(ctx, authority)
	if err != nil {
		return false, err
	}
	return access.CanUse(ctx, agentID)
}

func (s *Service) CanList(ctx context.Context, authority authz.Authority) error {
	a, err := s.Begin(ctx, authority)
	if err != nil {
		return err
	}
	return a.CanList()
}

func (s *Service) CanCreate(ctx context.Context, authority authz.Authority) error {
	a, err := s.Begin(ctx, authority)
	if err != nil {
		return err
	}
	return a.CanCreate()
}

func (s *Service) ListReadable(ctx context.Context, authority authz.Authority, deploymentWide bool) ([]config.Agent, error) {
	a, err := s.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	return a.ListReadable(ctx, deploymentWide)
}

// Authorize is the narrow cross-domain Agent port. It reads the durable Agent
// and applies the same direct Agent rules as the public access methods; a
// dedicated-channel binding is intentionally not inferred here.
func (s *Service) Authorize(ctx context.Context, authority authz.Authority, agentID string, action authz.Action) error {
	_, err := oneShot(s, ctx, authority, func(a *Access) (config.Agent, error) {
		return a.decide(ctx, agentID, action, false)
	})
	return err
}

func oneShot(s *Service, ctx context.Context, authority authz.Authority, fn func(*Access) (config.Agent, error)) (config.Agent, error) {
	a, err := s.Begin(ctx, authority)
	if err != nil {
		return config.Agent{}, err
	}
	return fn(a)
}

func (a *Access) decide(ctx context.Context, agentID string, action authz.Action, dedicated bool) (config.Agent, error) {
	ag, err := a.load(ctx, agentID)
	if err != nil {
		return config.Agent{}, err
	}
	return a.decideLoaded(ctx, ag, action, dedicated)
}

// decideLoaded is the sole Agent PEP decision path for both ordinary and
// snapshot reads. Snapshot callers supply a row read once; the policy remains
// identical to decide instead of being duplicated in management adapters.
func (a *Access) decideLoaded(ctx context.Context, ag config.Agent, action authz.Action, dedicated bool) (config.Agent, error) {
	if action == authz.ActionExecute && !ag.Enabled {
		return config.Agent{}, ErrForbidden
	}
	scope, err := canonicalScope(ag.Scope)
	if err != nil {
		return config.Agent{}, ErrForbidden
	}
	ok, err := a.allowed(ctx, ag, scope, action, dedicated)
	if err != nil {
		return config.Agent{}, err
	}
	if ok {
		return ag, nil
	}
	return config.Agent{}, ErrForbidden
}

// allowed applies the direct Agent rules. It consults the user's agent
// assignments only on the single path that needs them — an ordinary UserActor
// reading or using a non-system, non-dedicated agent. Admin, system-scope,
// dedicated-channel, delegated-agent, group-agent, and system actors are decided
// without touching the AssignmentStore, so their availability no longer couples to
// an assignment lookup. When the assignment lookup is required and fails, the
// error propagates and the decision fails closed.
func (a *Access) allowed(ctx context.Context, ag config.Agent, scope string, action authz.Action, dedicated bool) (bool, error) {
	switch action {
	case authz.ActionRead, authz.ActionExecute, authz.ActionManage, authz.ActionDelete:
	default:
		return false, nil
	}
	if a.authority.IsAdmin() {
		return true, nil
	}

	switch action {
	case authz.ActionRead, authz.ActionExecute:
		switch a.authority.Kind() {
		case authz.ActorUser:
			if scope == "system" || dedicated {
				return true, nil
			}
			return a.assignedTo(ctx, ag.ID)
		case authz.ActorGuest:
			return dedicated, nil
		case authz.ActorAgent, authz.ActorGroupAgent:
			return string(a.authority.AgentID()) == ag.ID, nil
		case authz.ActorSystem:
			return true, nil
		default:
			return false, nil
		}
	case authz.ActionManage, authz.ActionDelete:
		return a.isCreator(ag), nil
	default:
		return false, nil
	}
}

// isCreator is the ownership half of the Manage decision, without the admin
// superuser reach that decide() applies before ever getting here. Listing uses
// it directly, which is how an admin's own list stays their own fleet.
func (a *Access) isCreator(ag config.Agent) bool {
	return a.authority.Kind() == authz.ActorUser && a.userID != "" && a.userID == ag.CreatorID
}

func (a *Access) load(ctx context.Context, agentID string) (config.Agent, error) {
	ag, err := a.svc.agents.GetAgent(ctx, agentID)
	if err != nil {
		if isNotFound(err) {
			return config.Agent{}, ErrNotFound
		}
		return config.Agent{}, fmt.Errorf("%w: get agent: %w", ErrUnavailable, err)
	}
	return ag, nil
}

func (a *Access) loadSnapshot(ctx context.Context, agentID string) (config.AgentSnapshot, error) {
	store, ok := a.svc.agents.(AgentSnapshotStore)
	if !ok {
		return config.AgentSnapshot{}, fmt.Errorf("%w: agent snapshots are not wired", ErrUnavailable)
	}
	snapshot, err := store.GetAgentSnapshot(ctx, agentID)
	if err != nil {
		if isNotFound(err) {
			return config.AgentSnapshot{}, ErrNotFound
		}
		return config.AgentSnapshot{}, fmt.Errorf("%w: get agent snapshot: %w", ErrUnavailable, err)
	}
	return snapshot, nil
}

func (a *Access) assignedTo(ctx context.Context, agentID string) (bool, error) {
	if a.userID == "" {
		return false, nil
	}
	if !a.loaded {
		a.loaded = true
		ids, err := a.svc.assign.ListUserAgentIDs(ctx, a.userID)
		if err != nil {
			a.assignErr = err
		} else {
			a.assigned = make(map[string]bool, len(ids))
			for _, id := range ids {
				a.assigned[id] = true
			}
		}
	}
	if a.assignErr != nil {
		return false, fmt.Errorf("%w: list user agents: %w", ErrUnavailable, a.assignErr)
	}
	return a.assigned[agentID], nil
}

func canonicalScope(scope string) (string, error) {
	switch scope {
	case config.AgentScopeSystem:
		return "system", nil
	case config.AgentScopeRestricted, "user":
		return "user", nil
	case "shared":
		return "shared", nil
	default:
		return "", fmt.Errorf("unknown agent scope %q", scope)
	}
}

func isNotFound(err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}
