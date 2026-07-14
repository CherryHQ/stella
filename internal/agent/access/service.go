// Package access is the sole policy-enforcement point for Agent resources.
package access

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
)

var (
	ErrForbidden   = errors.New("agent access forbidden")
	ErrNotFound    = errors.New("agent not found")
	ErrUnavailable = errors.New("agent authorization unavailable")
)

// AgentStore is deliberately narrow, but includes list because policy-visible
// collection filtering belongs at the PEP rather than in transports or SQL.
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
	authz  authz.Authorizer
}

func NewService(agents AgentStore, assign AssignmentStore, az authz.Authorizer) *Service {
	return &Service{agents: agents, assign: assign, authz: az}
}

// Access is one privileged use case bound to exactly one policy evaluation.
type Access struct {
	svc       *Service
	eval      authz.Evaluation
	authority authz.Authority
	userID    string
	loaded    bool
	assigned  map[string]bool
	assignErr error
}

func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if !authority.Valid() {
		return nil, ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return &Access{svc: s, eval: eval, authority: authority, userID: string(authority.Actor().UserID())}, nil
}

func (a *Access) Revision() int64 { return a.eval.Revision() }
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
	if err != nil || !a.authority.HasGrant(grant) {
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

func (a *Access) CanList() error   { return a.decideGate(policy.AgentListRequest) }
func (a *Access) CanCreate() error { return a.decideGate(policy.AgentCreateRequest) }

// ListReadable applies both the collection list decision and a read decision to
// every candidate under this Access's single revision. SQL may narrow candidates
// for performance, but never decides visibility.
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
	status := "disabled"
	if ag.Enabled {
		status = "enabled"
	}
	facts := policy.AgentFacts{
		Scope: scope, Assigned: assigned, Creator: ag.CreatorID,
		IsCreator: a.userID != "" && ag.CreatorID == a.userID,
		// Exact actor/resource binding, including GroupAgentActor. A role can
		// never make this true.
		IsExecutor: string(a.authority.Actor().AgentID()) == ag.ID,
		Dedicated:  dedicated, Status: status,
	}
	var req authz.Request
	switch action {
	case authz.ActionRead:
		req, err = policy.AgentReadRequest(ag.ID, ag.CreatorID, facts)
	case authz.ActionExecute:
		req, err = policy.AgentUseRequest(ag.ID, ag.CreatorID, facts)
	case authz.ActionManage:
		req, err = policy.AgentManageRequest(ag.ID, ag.CreatorID, facts)
	case authz.ActionDelete:
		req, err = policy.AgentDeleteRequest(ag.ID, ag.CreatorID, facts)
	default:
		return config.Agent{}, ErrForbidden
	}
	if err != nil {
		return config.Agent{}, ErrForbidden
	}
	return a.finish(ag, req)
}

func (a *Access) finish(ag config.Agent, req authz.Request) (config.Agent, error) {
	dec, err := a.eval.Decide(req)
	if err != nil {
		return config.Agent{}, fmt.Errorf("%w: decide: %w", ErrUnavailable, err)
	}
	if !dec.Allowed() {
		return config.Agent{}, visibilityError(dec.Visibility())
	}
	return ag, nil
}

func (a *Access) decideGate(build func() (authz.Request, error)) error {
	req, err := build()
	if err != nil {
		return ErrForbidden
	}
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("%w: decide: %w", ErrUnavailable, err)
	}
	if !dec.Allowed() {
		return visibilityError(dec.Visibility())
	}
	return nil
}

// AuthorizeWithin authorizes an agent action against a caller's already-open
// evaluation, so a use case in another domain (goal/workflow/scheduler) can gate
// an agent under its single policy revision instead of opening a second
// evaluation. It never widens access: the agent facts are loaded from durable
// state and the passed Authority, and dedicated-channel use is not inferred here
// (that grant is only honored at the dedicated agent-use boundary).
func (s *Service) AuthorizeWithin(ctx context.Context, eval authz.Evaluation, authority authz.Authority, agentID string, action authz.Action) error {
	ag, err := s.agents.GetAgent(ctx, agentID)
	if err != nil {
		if isNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("%w: get agent: %w", ErrUnavailable, err)
	}
	scope, err := canonicalScope(ag.Scope)
	if err != nil {
		return ErrForbidden
	}
	userID := string(authority.Actor().UserID())
	assigned := false
	// System-scoped agents are readable without an assignment; skip the query.
	if scope != "system" && userID != "" {
		ids, err := s.assign.ListUserAgentIDs(ctx, userID)
		if err != nil {
			return fmt.Errorf("%w: list user agents: %w", ErrUnavailable, err)
		}
		assigned = slices.Contains(ids, agentID)
	}
	status := "disabled"
	if ag.Enabled {
		status = "enabled"
	}
	facts := policy.AgentFacts{
		Scope: scope, Assigned: assigned, Creator: ag.CreatorID,
		IsCreator:  userID != "" && ag.CreatorID == userID,
		IsExecutor: string(authority.Actor().AgentID()) == ag.ID,
		Status:     status,
	}
	var req authz.Request
	switch action {
	case authz.ActionRead:
		req, err = policy.AgentReadRequest(ag.ID, ag.CreatorID, facts)
	case authz.ActionExecute:
		req, err = policy.AgentUseRequest(ag.ID, ag.CreatorID, facts)
	default:
		return ErrForbidden
	}
	if err != nil {
		return ErrForbidden
	}
	dec, err := eval.Decide(req)
	if err != nil {
		return fmt.Errorf("%w: decide: %w", ErrUnavailable, err)
	}
	if !dec.Allowed() {
		return visibilityError(dec.Visibility())
	}
	return nil
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

func visibilityError(v authz.Visibility) error {
	if v == authz.VisibilityHidden {
		return ErrNotFound
	}
	return ErrForbidden
}

func isNotFound(err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}
