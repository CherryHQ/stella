// Package sessionaccess is the authoritative Session and Workspace application
// service. It owns durable session lookup, session-registry access, workspace
// materialization, and the policy enforcement point for those resources.
//
// Transport code passes a trusted authz.Authority and typed input; it never
// receives a sqlc query handle, memory.SessionManager, config.Store, or asset
// store. An Access is one use case and binds exactly one Authorizer evaluation.
package sessionaccess

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var (
	ErrForbidden   = errors.New("session access forbidden")
	ErrNotFound    = errors.New("session not found")
	ErrUnavailable = errors.New("session authorization unavailable")
)

// Service owns Session/Workspace use cases and their narrow persistence ports.
type AssignmentStore interface {
	ListUserAgentIDs(context.Context, string) ([]string, error)
}

type Service struct {
	registry *agentsession.Registry
	memory   memory.SessionManager
	q        *sqlc.Queries
	store    config.Store
	assign   AssignmentStore
	assets   *asset.Store
	authz    authz.Authorizer
	runtime  RuntimeManager
	prompts  SystemPromptBuilder
}

type Option func(*Service)

func WithSystemPromptBuilder(builder SystemPromptBuilder) Option {
	return func(s *Service) { s.prompts = builder }
}

// NewService constructs the only Session/Workspace PEP. The registry remains
// the canonical lifecycle owner; this service owns its policy-scoped use.
func NewService(mem memory.Provider, db sqlc.DBTX, store config.Store, assign AssignmentStore, assets *asset.Store, az authz.Authorizer, opts ...Option) (*Service, error) {
	if mem == nil || db == nil || store == nil || assign == nil || assets == nil || az == nil {
		return nil, fmt.Errorf("session access: missing dependency")
	}
	sm, ok := mem.(memory.SessionManager)
	if !ok {
		return nil, fmt.Errorf("session access: memory provider does not implement SessionManager")
	}
	registry, err := agentsession.NewRegistry(mem, "")
	if err != nil {
		return nil, fmt.Errorf("session access: registry: %w", err)
	}
	svc := &Service{registry: registry, memory: sm, q: sqlc.New(db), store: store, assign: assign, assets: assets, authz: az}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	return svc, nil
}

// GetSystemPrompt resolves, authorizes, and builds a session's effective system
// prompt behind the session PEP.
func (s *Service) GetSystemPrompt(ctx context.Context, in SystemPromptInput) (string, error) {
	if s.prompts == nil {
		return "", fmt.Errorf("%w: system prompt builder not configured", ErrUnavailable)
	}
	access, err := s.Begin(ctx, in.Authority)
	if err != nil {
		return "", err
	}
	info, err := access.Read(ctx, in.AgentID, in.SessionID)
	if err != nil {
		return "", err
	}
	return s.prompts.BuildSessionSystemPrompt(ctx, SystemPromptBuildInput{Info: info})
}

// Access is a single policy revision. Do not retain it across use cases.
type Access struct {
	svc       *Service
	eval      authz.Evaluation
	authority authz.Authority
}

// Begin creates exactly one evaluation for one Session/Workspace use case.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if !authority.Valid() {
		return nil, ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("%w: begin: %w", ErrUnavailable, err)
	}
	return &Access{svc: s, eval: eval, authority: authority}, nil
}

// Read resolves a routed session and authorizes reading it.
func (a *Access) Read(ctx context.Context, agentID, sessionID string) (agentsession.Info, error) {
	return a.session(ctx, agentID, sessionID, authz.ActionRead)
}

// Detail resolves an authorized session and the non-sensitive display facts the
// HTTP representation needs. Configuration lookup remains inside the domain
// service; transports never reach config.Store for a session.
type Detail struct {
	Info      agentsession.Info
	AgentName string
}

func (a *Access) Detail(ctx context.Context, agentID, sessionID string) (Detail, error) {
	info, err := a.Read(ctx, agentID, sessionID)
	if err != nil {
		return Detail{}, err
	}
	agent, err := a.svc.store.GetAgent(ctx, info.AgentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Detail{Info: info}, nil
		}
		return Detail{}, fmt.Errorf("%w: get session agent: %w", ErrUnavailable, err)
	}
	return Detail{Info: info, AgentName: agent.Name}, nil
}

// ProjectRoot authorizes the referenced session before resolving its project.
// Callers that merely use a session ID as optional context therefore cannot
// bypass session visibility through project-scoped features such as skills.
func (a *Access) ProjectRoot(ctx context.Context, agentID string, sessionID *string) (string, error) {
	if sessionID == nil || *sessionID == "" {
		return "", nil
	}
	info, err := a.Read(ctx, agentID, *sessionID)
	if err != nil {
		return "", err
	}
	if info.ProjectID == "" {
		return "", nil
	}
	project, err := a.svc.q.GetProject(ctx, sqlc.GetProjectParams{ID: info.ProjectID, UserID: info.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: get session project: %w", ErrUnavailable, err)
	}
	return project.BaseDir, nil
}

// Use resolves a routed session and authorizes a turn on it.
func (a *Access) Use(ctx context.Context, agentID, sessionID string) (agentsession.Info, error) {
	return a.session(ctx, agentID, sessionID, authz.ActionExecute)
}

// EnsureUse resolves or creates a session through the registry, then authorizes
// executing a turn on the exact durable session. Exact-ID creation is allowed
// only when the caller's trusted Authority is already bound to that entry class
// (channel/group/worker/system); untrusted transports must use Create/ResolveMain.
func (a *Access) EnsureUse(ctx context.Context, req agentsession.Request) (agentsession.Info, error) {
	return a.ensure(ctx, req, authz.ActionExecute)
}

// EnsureRead resolves or creates a session through the registry, then authorizes
// reading it. Channel session pre-resolution uses this to materialize the same
// durable facts later used by the turn.
func (a *Access) EnsureRead(ctx context.Context, req agentsession.Request) (agentsession.Info, error) {
	return a.ensure(ctx, req, authz.ActionRead)
}

// Write resolves a routed session and authorizes a metadata mutation.
func (a *Access) Write(ctx context.Context, agentID, sessionID string) (agentsession.Info, error) {
	return a.session(ctx, agentID, sessionID, authz.ActionWrite)
}

// Delete resolves a routed session and authorizes archival/deletion.
func (a *Access) Delete(ctx context.Context, agentID, sessionID string) (agentsession.Info, error) {
	return a.session(ctx, agentID, sessionID, authz.ActionDelete)
}

// Workspace authorizes a workspace operation under the session's durable facts.
// It evaluates both the Session read and the Workspace action in this same
// Access; there is deliberately no second Begin between them.
func (a *Access) Workspace(ctx context.Context, agentID, sessionID string, action authz.Action) (agentsession.Info, error) {
	info, err := a.session(ctx, agentID, sessionID, authz.ActionRead)
	if err != nil {
		return agentsession.Info{}, err
	}
	request, err := policy.WorkspaceRequest(action, info.ID, info.UserID, factsFor(info, a.authority))
	if err != nil {
		return agentsession.Info{}, ErrForbidden
	}
	if err := a.decide(request); err != nil {
		return agentsession.Info{}, err
	}
	return info, nil
}

// Create creates a user-owned session after one create decision. It uses the
// registry rather than making a second, transport-owned persistence path.
func (a *Access) Create(ctx context.Context, userID, agentID, projectID string, kind agentsession.Kind, channel agentsession.Channel) (agentsession.Info, error) {
	if userID == "" || agentID == "" || string(a.authority.Actor().UserID()) != userID {
		return agentsession.Info{}, ErrForbidden
	}
	if err := a.authorizeAgent(ctx, agentID, authz.ActionRead); err != nil {
		return agentsession.Info{}, err
	}
	actor := a.authority.Actor()
	facts := policy.SessionFacts{
		Owner: userID, Agent: agentID, Kind: string(kind), State: "active", IsOwner: true,
		IsExecutor: string(actor.AgentID()) != "" && string(actor.AgentID()) == agentID,
	}
	request, err := policy.SessionCreateRequest(userID, facts)
	if err != nil {
		return agentsession.Info{}, ErrForbidden
	}
	if err := a.decide(request); err != nil {
		return agentsession.Info{}, err
	}
	return a.svc.registry.Ensure(ctx, agentsession.Request{UserID: userID, AgentID: agentID, ProjectID: projectID, Kind: kind, Channel: channel, CreateIfMissing: true})
}

// ResolveMain resolves the caller's durable main session in this Access.
func (a *Access) ResolveMain(ctx context.Context, userID, agentID string) (agentsession.Info, error) {
	if userID == "" || agentID == "" || string(a.authority.Actor().UserID()) != userID {
		return agentsession.Info{}, ErrForbidden
	}
	if err := a.authorizeAgent(ctx, agentID, authz.ActionRead); err != nil {
		return agentsession.Info{}, err
	}
	actor := a.authority.Actor()
	facts := policy.SessionFacts{
		Owner: userID, Agent: agentID, Kind: string(agentsession.KindMain), State: "active", IsOwner: true,
		IsExecutor: string(actor.AgentID()) != "" && string(actor.AgentID()) == agentID,
	}
	request, err := policy.SessionCreateRequest(userID, facts)
	if err != nil {
		return agentsession.Info{}, ErrForbidden
	}
	if err := a.decide(request); err != nil {
		return agentsession.Info{}, err
	}
	return a.svc.registry.ResolveMain(ctx, agentsession.MainRequest{UserID: userID, AgentID: agentID})
}

// List lists the actor's sessions and filters every row through the same
// evaluation. Collection visibility and individual visibility therefore cannot
// drift apart.
func (a *Access) List(ctx context.Context, agentID string, opts agentsession.ListOptions) ([]agentsession.Info, error) {
	request, err := policy.SessionListRequest()
	if err != nil {
		return nil, ErrForbidden
	}
	if err := a.decide(request); err != nil {
		return nil, err
	}
	userID := string(a.authority.Actor().UserID())
	if userID == "" {
		return nil, ErrForbidden
	}
	infos, err := a.svc.registry.List(ctx, agentsession.Scope{UserID: userID, AgentID: agentID}, opts)
	if err != nil {
		return nil, fmt.Errorf("%w: list sessions: %w", ErrUnavailable, err)
	}
	out := infos[:0]
	for _, info := range infos {
		request, err := policy.SessionReadRequest(info.ID, info.UserID, factsFor(info, a.authority))
		if err != nil {
			return nil, ErrForbidden
		}
		if err := a.decide(request); err == nil {
			out = append(out, info)
		} else if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return out, nil
}

// UpdateTitle persists a title after Write has authorized the exact session.
func (a *Access) UpdateTitle(ctx context.Context, info agentsession.Info, title string) error {
	if err := a.svc.q.UpdateConversationTitleBySessionID(ctx, sqlc.UpdateConversationTitleBySessionIDParams{
		Title: pgtype.Text{String: title, Valid: true}, SessionID: info.ID,
		UserID: pgtype.Text{String: info.UserID, Valid: true}, AgentID: pgtype.Text{String: info.AgentID, Valid: true},
	}); err != nil {
		return fmt.Errorf("%w: update session title: %w", ErrUnavailable, err)
	}
	return nil
}

// Archive persists archival after Delete has authorized the exact session.
func (a *Access) Archive(ctx context.Context, info agentsession.Info) error {
	if err := a.svc.q.UpdateConversationArchived(ctx, sqlc.UpdateConversationArchivedParams{
		Archived: true, SessionID: info.ID,
		UserID: pgtype.Text{String: info.UserID, Valid: true}, AgentID: pgtype.Text{String: info.AgentID, Valid: true},
	}); err != nil {
		return fmt.Errorf("%w: archive session: %w", ErrUnavailable, err)
	}
	return nil
}

func (a *Access) ensure(ctx context.Context, req agentsession.Request, action authz.Action) (agentsession.Info, error) {
	if req.AgentID == "" || (req.ID == "" && !req.CreateIfMissing) {
		return agentsession.Info{}, ErrNotFound
	}
	if req.CreateIfMissing {
		if req.UserID == "" || req.Kind == "" || req.Channel == "" {
			return agentsession.Info{}, ErrForbidden
		}
		if err := a.authorizeAgent(ctx, req.AgentID, agentAction(action)); err != nil {
			return agentsession.Info{}, err
		}
		facts := policy.SessionFacts{
			Owner:       req.UserID,
			Agent:       req.AgentID,
			Kind:        string(req.Kind),
			State:       "active",
			IsOwner:     string(a.authority.Actor().UserID()) != "" && string(a.authority.Actor().UserID()) == req.UserID,
			IsExecutor:  string(a.authority.Actor().AgentID()) != "" && string(a.authority.Actor().AgentID()) == req.AgentID,
			IsGroup:     req.GroupID != "",
			IsSameGroup: req.GroupID != "" && string(a.authority.Actor().GroupID()) == req.GroupID,
		}
		createReq, err := policy.SessionCreateRequest(req.UserID, facts)
		if err != nil {
			return agentsession.Info{}, ErrForbidden
		}
		if err := a.decide(createReq); err != nil {
			return agentsession.Info{}, err
		}
	}
	info, err := a.svc.registry.Ensure(ctx, req)
	if err != nil {
		return agentsession.Info{}, fmt.Errorf("%w: resolve session: %w", ErrNotFound, err)
	}
	if req.ProjectID != "" && info.ProjectID != req.ProjectID {
		return agentsession.Info{}, ErrForbidden
	}
	if req.GroupID != "" && info.GroupID != req.GroupID {
		return agentsession.Info{}, ErrForbidden
	}
	if req.UserID != "" && info.UserID != req.UserID {
		return agentsession.Info{}, ErrForbidden
	}
	if req.AgentID != "" && info.AgentID != req.AgentID {
		return agentsession.Info{}, ErrForbidden
	}
	return a.authorizeInfo(ctx, info, action)
}

func (a *Access) authorizeInfo(ctx context.Context, info agentsession.Info, action authz.Action) (agentsession.Info, error) {
	if err := a.authorizeAgent(ctx, info.AgentID, agentAction(action)); err != nil {
		return agentsession.Info{}, err
	}
	request, err := policy.SessionRequest(action, info.ID, info.UserID, factsFor(info, a.authority))
	if err != nil {
		return agentsession.Info{}, ErrForbidden
	}
	if err := a.decide(request); err != nil {
		return agentsession.Info{}, err
	}
	return info, nil
}

func (a *Access) session(ctx context.Context, routeAgentID, sessionID string, action authz.Action) (agentsession.Info, error) {
	if sessionID == "" {
		return agentsession.Info{}, ErrNotFound
	}
	// This unscoped lookup obtains durable ownership only; it is private to the
	// PEP. The subsequent scoped SessionManager load preserves the memory-layer
	// tenant boundary and makes malformed records fail closed.
	conv, err := a.svc.q.GetConversationForSessionAccess(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agentsession.Info{}, ErrNotFound
		}
		return agentsession.Info{}, fmt.Errorf("%w: get session facts: %w", ErrUnavailable, err)
	}
	if !conv.UserID.Valid || !conv.AgentID.Valid || (routeAgentID != "" && routeAgentID != conv.AgentID.String) {
		return agentsession.Info{}, ErrNotFound
	}
	loadCtx := authz.WithAgentID(authz.WithUserID(ctx, conv.UserID.String), conv.AgentID.String)
	record, err := a.svc.memory.LoadInfo(loadCtx, sessionID)
	if err != nil {
		return agentsession.Info{}, ErrNotFound
	}
	info, err := agentsession.InfoFromRecord(record)
	if err != nil {
		return agentsession.Info{}, fmt.Errorf("%w: invalid session record: %w", ErrUnavailable, err)
	}
	return a.authorizeInfo(ctx, info, action)
}

func agentAction(sessionAction authz.Action) authz.Action {
	if sessionAction == authz.ActionExecute {
		return authz.ActionExecute
	}
	return authz.ActionRead
}

// authorizeAgent uses this Access's evaluation rather than calling
// agentaccess.Service, so a Session use case cannot observe two policy
// revisions between validating the persisted session and authorizing its agent.
func (a *Access) authorizeAgent(ctx context.Context, agentID string, action authz.Action) error {
	agent, err := a.svc.store.GetAgent(ctx, agentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("%w: get agent: %w", ErrUnavailable, err)
	}
	scope, ok := map[string]string{config.AgentScopeSystem: "system", config.AgentScopeRestricted: "user", "user": "user", "shared": "shared"}[agent.Scope]
	if !ok {
		return ErrForbidden
	}
	assigned := false
	// System-scoped agents are readable by a user without an assignment. Avoid a
	// needless assignment query (and keep policy evaluation available while the
	// assignment relation is temporarily unavailable) for that built-in path.
	if scope != "system" && string(a.authority.Actor().UserID()) != "" {
		ids, err := a.svc.assign.ListUserAgentIDs(ctx, string(a.authority.Actor().UserID()))
		if err != nil {
			return fmt.Errorf("%w: list agent assignments: %w", ErrUnavailable, err)
		}
		if slices.Contains(ids, agentID) {
			assigned = true
		}
	}
	status := "disabled"
	if agent.Enabled {
		status = "enabled"
	}
	actorUserID := string(a.authority.Actor().UserID())
	dedicated, err := a.dedicatedTo(ctx, agentID)
	if err != nil {
		return err
	}
	facts := policy.AgentFacts{
		Scope: scope, Assigned: assigned, Creator: agent.CreatorID,
		IsCreator:  actorUserID != "" && actorUserID == agent.CreatorID,
		IsExecutor: string(a.authority.Actor().AgentID()) == agentID,
		Dedicated:  dedicated, Status: status,
	}
	var request authz.Request
	switch action {
	case authz.ActionRead:
		request, err = policy.AgentReadRequest(agentID, agent.CreatorID, facts)
	case authz.ActionExecute:
		request, err = policy.AgentUseRequest(agentID, agent.CreatorID, facts)
	default:
		return ErrForbidden
	}
	if err != nil {
		return ErrForbidden
	}
	return a.decide(request)
}

// dedicatedTo derives dedicated-channel use only from an immutable binding
// grant plus the current persisted channel-to-agent binding. A stale or
// unrelated grant never widens agent visibility.
func (a *Access) dedicatedTo(ctx context.Context, agentID string) (bool, error) {
	for _, grant := range a.authority.Grants() {
		if grant.Kind() != authz.GrantChannelBinding {
			continue
		}
		channel, err := a.svc.store.GetChannel(ctx, grant.Key())
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("%w: get dedicated channel: %w", ErrUnavailable, err)
		}
		if channel.AgentID == agentID {
			return true, nil
		}
	}
	return false, nil
}

func (a *Access) decide(request authz.Request) error {
	decision, err := a.eval.Decide(request)
	if err != nil {
		return fmt.Errorf("%w: decide: %w", ErrUnavailable, err)
	}
	if !decision.Allowed() {
		// Sessions are intentionally opaque: an otherwise-visible agent must not
		// reveal historical session existence after access is revoked.
		return ErrNotFound
	}
	return nil
}

func factsFor(info agentsession.Info, authority authz.Authority) policy.SessionFacts {
	actor := authority.Actor()
	return policy.SessionFacts{
		Owner:       info.UserID,
		Agent:       info.AgentID,
		Kind:        info.Kind,
		State:       sessionState(info),
		IsOwner:     string(actor.UserID()) != "" && string(actor.UserID()) == info.UserID,
		IsExecutor:  string(actor.AgentID()) != "" && string(actor.AgentID()) == info.AgentID,
		IsGroup:     info.GroupID != "",
		IsSameGroup: info.GroupID != "" && string(actor.GroupID()) == info.GroupID,
	}
}

func sessionState(info agentsession.Info) string {
	if info.Archived {
		return "archived"
	}
	return "active"
}
