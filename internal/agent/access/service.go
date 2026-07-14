// Package access is the policy-enforcement point for Agent resources.
package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
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

type AssignmentStore interface {
	ListUserAgentIDs(ctx context.Context, userID string) ([]string, error)
}

// dedicatedChannelStore is intentionally asserted at the dedicated-use boundary:
// a channel-binding grant alone is not authority for an arbitrary agent.
type dedicatedChannelStore interface {
	GetChannel(ctx context.Context, id string) (config.Channel, error)
}

type Service struct {
	agents AgentStore
	assign AssignmentStore
}

func NewService(agents AgentStore, assign AssignmentStore) *Service {
	return &Service{agents: agents, assign: assign}
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
	return &Access{svc: s, authority: authority, userID: string(authority.Actor().UserID())}, nil
}

func (a *Access) Read(ctx context.Context, agentID string) (config.Agent, error) {
	return a.decide(ctx, agentID, authz.ActionRead, false)
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

// UseDedicated requires both an exact persisted channel-binding grant and a
// current DB binding from that channel to this exact agent. Either mismatch
// fails closed; channel ID by itself is never an agent-use capability.
func (a *Access) UseDedicated(ctx context.Context, agentID, channelID string) (config.Agent, error) {
	grant, err := authz.ChannelBindingGrant(channelID)
	if err != nil || a.authority.Kind() != authz.ActorUser || !a.authority.HasRole(authz.RoleUser) || !a.authority.HasGrant(grant) {
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
	if channel.AgentID != agentID {
		return config.Agent{}, ErrForbidden
	}
	return a.decide(ctx, agentID, authz.ActionExecute, true)
}

// AuthorizeViaChannelBinding authorizes read/execute of agentID for a trusted
// dedicated-channel Authority: any held ChannelBinding grant whose CURRENT
// persisted channel→agent binding names this exact agent. It is the Agent PEP's
// sole interpretation of a dedicated channel, so cross-domain callers (the
// session flow's Agent gate) never re-derive channel bindings themselves. A held
// grant alone is never authority for an arbitrary agent; every mismatch fails
// closed.
func (s *Service) AuthorizeViaChannelBinding(ctx context.Context, authority authz.Authority, agentID string) error {
	a, err := s.Begin(ctx, authority)
	if err != nil {
		return err
	}
	for _, grant := range authority.Grants() {
		if grant.Kind() != authz.GrantChannelBinding {
			continue
		}
		switch _, err := a.UseDedicated(ctx, agentID, grant.Key()); {
		case err == nil:
			return nil
		case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound):
			continue
		default:
			return err
		}
	}
	return ErrForbidden
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
	if a.authority.IsAdmin() || (a.authority.Kind() == authz.ActorUser && a.authority.HasRole(authz.RoleUser)) {
		return nil
	}
	return ErrForbidden
}

func (a *Access) CanCreate() error {
	if a.authority.IsAdmin() || (a.authority.Kind() == authz.ActorUser && a.authority.HasRole(authz.RoleUser)) {
		return nil
	}
	return ErrForbidden
}

// ListReadable applies both the collection list decision and a read decision to
// every candidate. SQL may narrow candidates for performance, but never decides
// visibility.
func (a *Access) ListReadable(ctx context.Context, includeDisabled bool) ([]config.Agent, error) {
	if err := a.CanList(); err != nil {
		return nil, err
	}
	agents, err := a.svc.agents.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: list agents: %w", ErrUnavailable, err)
	}
	out := make([]config.Agent, 0, len(agents))
	for _, agent := range agents {
		if !includeDisabled && !agent.Enabled {
			continue
		}
		if _, err := a.Read(ctx, agent.ID); err == nil {
			out = append(out, agent)
		} else if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return out, nil
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

func (s *Service) ListReadable(ctx context.Context, authority authz.Authority, includeDisabled bool) ([]config.Agent, error) {
	a, err := s.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	return a.ListReadable(ctx, includeDisabled)
}

// Authorize is the narrow cross-domain Agent port. It reads the durable Agent
// and applies the same direct Agent rules as the public access methods; a
// dedicated-channel grant is intentionally not inferred here.
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
	assigned, err := a.assignedTo(ctx, agentID)
	if err != nil {
		return config.Agent{}, err
	}
	scope, err := canonicalScope(ag.Scope)
	if err != nil {
		return config.Agent{}, ErrForbidden
	}
	if a.allowed(ag, scope, assigned, action, dedicated) {
		return ag, nil
	}
	return config.Agent{}, ErrForbidden
}

func (a *Access) allowed(ag config.Agent, scope string, assigned bool, action authz.Action, dedicated bool) bool {
	switch action {
	case authz.ActionRead, authz.ActionExecute, authz.ActionManage, authz.ActionDelete:
	default:
		return false
	}
	if a.authority.IsAdmin() {
		return true
	}

	switch action {
	case authz.ActionRead, authz.ActionExecute:
		switch a.authority.Kind() {
		case authz.ActorUser:
			return a.authority.HasRole(authz.RoleUser) && (scope == "system" || assigned || dedicated)
		case authz.ActorAgent:
			return string(a.authority.Actor().AgentID()) == ag.ID
		case authz.ActorGroupAgent:
			grant, err := authz.GroupToolGrant("agent.use")
			return err == nil && a.authority.HasGrant(grant) && string(a.authority.Actor().AgentID()) == ag.ID
		case authz.ActorSystem:
			grant, err := authz.SystemGrant("agent.use")
			return err == nil && a.authority.HasGrant(grant)
		default:
			return false
		}
	case authz.ActionManage, authz.ActionDelete:
		return a.authority.Kind() == authz.ActorUser && a.authority.HasRole(authz.RoleUser) && a.userID != "" && a.userID == ag.CreatorID
	default:
		return false
	}
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
