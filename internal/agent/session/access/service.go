// Package access is the authoritative Session and Workspace application
// service. It owns durable session lookup, session-registry access, workspace
// materialization, and the policy enforcement point for those resources.
//
// Transport code passes a trusted authz.Authority and typed input; it never
// receives a sqlc query handle, memory.SessionManager, config.Store, or asset
// store. Session and Workspace decisions are direct, domain-owned rules; every
// Agent gate is delegated to the Agent PEP so those rules live in one place.
package access

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
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
// Agent authorization is delegated wholesale to agents so the two domains cannot
// drift; this service never re-implements an Agent rule.
type Service struct {
	registry *agentsession.Registry
	memory   memory.SessionManager
	q        *sqlc.Queries
	store    config.Store
	agents   *agentaccess.Service
	assets   *asset.Store
	runtime  RuntimeManager
	prompts  SystemPromptBuilder
}

type Option func(*Service)

func WithSystemPromptBuilder(builder SystemPromptBuilder) Option {
	return func(s *Service) { s.prompts = builder }
}

// NewService constructs the only Session/Workspace PEP. The registry remains the
// canonical lifecycle owner; this service owns its policy-scoped use and routes
// every Agent decision through the shared Agent PEP.
func NewService(mem memory.Provider, db sqlc.DBTX, store config.Store, assets *asset.Store, agents *agentaccess.Service, opts ...Option) (*Service, error) {
	if mem == nil || db == nil || store == nil || assets == nil || agents == nil {
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
	svc := &Service{registry: registry, memory: sm, q: sqlc.New(db), store: store, agents: agents, assets: assets}
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

// Access captures one validated Authority for a single Session/Workspace use
// case. It holds no evaluation or revision; each decision reads the durable
// facts it needs when it runs. Do not retain it across use cases.
type Access struct {
	svc       *Service
	authority authz.Authority
}

// Begin validates the Authority for one Session/Workspace use case. The context
// is unused today; it stays in the signature so the agent runtime's session PEP
// port and every transport caller remain identical.
func (s *Service) Begin(_ context.Context, authority authz.Authority) (*Access, error) {
	if !authority.Valid() {
		return nil, ErrForbidden
	}
	return &Access{svc: s, authority: authority}, nil
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
// It reuses the Session read gate and then applies the direct Workspace rule in
// this same Access; there is deliberately no second Begin between them.
func (a *Access) Workspace(ctx context.Context, agentID, sessionID string, action authz.Action) (agentsession.Info, error) {
	info, err := a.session(ctx, agentID, sessionID, authz.ActionRead)
	if err != nil {
		return agentsession.Info{}, err
	}
	if !a.allowWorkspace(action, sessionFactsFor(info, a.authority)) {
		return agentsession.Info{}, ErrNotFound
	}
	return info, nil
}

// Create creates a user-owned session after one create decision. It uses the
// registry rather than making a second, transport-owned persistence path.
func (a *Access) Create(ctx context.Context, userID, agentID, projectID string, kind agentsession.Kind, channel agentsession.Channel) (agentsession.Info, error) {
	if userID == "" || agentID == "" || string(a.authority.UserID()) != userID {
		return agentsession.Info{}, ErrForbidden
	}
	if err := a.authorizeAgent(ctx, agentID, authz.ActionRead); err != nil {
		return agentsession.Info{}, err
	}
	actor := a.authority
	facts := sessionFacts{
		isOwner:    true,
		isExecutor: string(actor.AgentID()) != "" && string(actor.AgentID()) == agentID,
	}
	if !a.allowSession(authz.ActionCreate, facts) {
		return agentsession.Info{}, ErrNotFound
	}
	return a.svc.registry.Ensure(ctx, agentsession.Request{UserID: userID, AgentID: agentID, ProjectID: projectID, Kind: kind, Channel: channel, CreateIfMissing: true})
}

// ResolveMain resolves the caller's durable main session in this Access.
func (a *Access) ResolveMain(ctx context.Context, userID, agentID string) (agentsession.Info, error) {
	if userID == "" || agentID == "" || string(a.authority.UserID()) != userID {
		return agentsession.Info{}, ErrForbidden
	}
	if err := a.authorizeAgent(ctx, agentID, authz.ActionRead); err != nil {
		return agentsession.Info{}, err
	}
	actor := a.authority
	facts := sessionFacts{
		isOwner:    true,
		isExecutor: string(actor.AgentID()) != "" && string(actor.AgentID()) == agentID,
	}
	if !a.allowSession(authz.ActionCreate, facts) {
		return agentsession.Info{}, ErrNotFound
	}
	return a.svc.registry.ResolveMain(ctx, agentsession.MainRequest{UserID: userID, AgentID: agentID})
}

// RotateMain archives the caller's current main session and returns its
// successor. Both halves are decided here under one evaluation — Delete on the
// session being retired and Create on the one replacing it — because the
// successor does not exist yet and the predecessor is chosen by the registry:
// no caller can pre-authorize an Info and hand it to the lifecycle layer.
// expectedSessionID, when set, makes this a compare-and-rotate.
func (a *Access) RotateMain(ctx context.Context, userID, agentID, expectedSessionID string) (agentsession.Info, error) {
	if userID == "" || agentID == "" || string(a.authority.UserID()) != userID {
		return agentsession.Info{}, ErrForbidden
	}
	if err := a.authorizeAgent(ctx, agentID, authz.ActionRead); err != nil {
		return agentsession.Info{}, err
	}
	actor := a.authority
	facts := sessionFacts{
		isOwner:    true,
		isExecutor: string(actor.AgentID()) != "" && string(actor.AgentID()) == agentID,
	}
	if !a.allowSession(authz.ActionDelete, facts) || !a.allowSession(authz.ActionCreate, facts) {
		return agentsession.Info{}, ErrNotFound
	}
	return a.svc.registry.RotateMain(ctx, agentsession.MainRequest{UserID: userID, AgentID: agentID, ExpectedSessionID: expectedSessionID})
}

// ResolveChatChannel resolves the session a chat channel is currently bound to,
// creating one when the binding has none yet. It is the channel adapters' entry
// point: unlike EnsureUse it never treats a derived key as a session id, so the
// chat can rotate onto a successor without changing identity.
func (a *Access) ResolveChatChannel(ctx context.Context, req agentsession.ChannelRequest) (agentsession.Info, error) {
	if err := a.authorizeChannelBinding(ctx, req, authz.ActionCreate); err != nil {
		return agentsession.Info{}, err
	}
	if req.GuestID != "" {
		ctx = authz.WithGuestID(ctx, req.GuestID)
	}
	info, err := a.svc.registry.ResolveChatChannel(ctx, req)
	if err != nil {
		return agentsession.Info{}, err
	}
	if err := assertChannelBinding(req, info); err != nil {
		return agentsession.Info{}, err
	}
	return a.authorizeInfo(ctx, info, authz.ActionRead)
}

// RotateChannel archives a chat channel's current session and returns its
// successor. Both halves are decided here under one evaluation — Delete on the
// session being retired and Create on the one replacing it — because the
// successor does not exist yet and the predecessor is chosen by the registry.
// req.ExpectedSessionID, when set, makes this a compare-and-rotate.
func (a *Access) RotateChannel(ctx context.Context, req agentsession.ChannelRequest) (agentsession.Info, error) {
	if err := a.authorizeChannelBinding(ctx, req, authz.ActionDelete); err != nil {
		return agentsession.Info{}, err
	}
	if req.GuestID != "" {
		ctx = authz.WithGuestID(ctx, req.GuestID)
	}
	info, err := a.svc.registry.RotateChannel(ctx, req)
	if err != nil {
		return agentsession.Info{}, err
	}
	if err := assertChannelBinding(req, info); err != nil {
		return agentsession.Info{}, err
	}
	return info, nil
}

// authorizeChannelBinding decides a chat-channel binding operation from the
// binding's own facts. The session it names does not exist yet (or is chosen by
// the registry), so the decision is made on the binding rather than on an Info a
// caller could have pre-authorized. Create is always required; extra is the
// additional action a mutating use case needs.
func (a *Access) authorizeChannelBinding(ctx context.Context, req agentsession.ChannelRequest, extra authz.Action) error {
	if req.UserID == "" || req.AgentID == "" {
		return ErrForbidden
	}
	if req.GroupID != "" && req.GroupID != req.UserID {
		return ErrForbidden
	}
	if err := a.authorizeAgent(ctx, req.AgentID, authz.ActionRead); err != nil {
		return err
	}
	actor := a.authority
	facts := sessionFacts{
		isOwner:        string(actor.UserID()) != "" && string(actor.UserID()) == req.UserID,
		isGuestSession: req.GuestID != "" && req.UserID == req.GuestID,
		isSameGuest:    req.GuestID != "" && string(actor.GuestID()) == req.GuestID,
		isExecutor:     string(actor.AgentID()) != "" && string(actor.AgentID()) == req.AgentID,
		isGroup:        req.GroupID != "",
		isSameGroup:    req.GroupID != "" && string(actor.GroupID()) == req.GroupID,
	}
	if !a.allowSession(authz.ActionCreate, facts) || !a.allowSession(extra, facts) {
		return ErrNotFound
	}
	return nil
}

// assertChannelBinding fails closed when the registry hands back a session that
// does not carry the binding that was authorized.
func assertChannelBinding(req agentsession.ChannelRequest, info agentsession.Info) error {
	if info.UserID != req.UserID || info.AgentID != req.AgentID || info.GroupID != req.GroupID || info.GuestID != req.GuestID {
		return ErrForbidden
	}
	return nil
}

// List lists the actor's sessions and filters every row through the same
// evaluation. Collection visibility and individual visibility therefore cannot
// drift apart.
func (a *Access) List(ctx context.Context, agentID string, opts agentsession.ListOptions) ([]agentsession.Info, error) {
	if !a.allowSessionList() {
		return nil, ErrNotFound
	}
	if err := a.allowSessionListAgent(agentID); err != nil {
		return nil, err
	}
	userID := string(a.authority.UserID())
	if userID == "" {
		return nil, ErrForbidden
	}
	var infos []agentsession.Info
	var err error
	if a.authority.IsAdmin() {
		infos, err = a.svc.registry.ListForAdmin(ctx, userID, agentID, opts)
	} else {
		infos, err = a.svc.registry.List(ctx, agentsession.Scope{UserID: userID, AgentID: agentID}, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: list sessions: %w", ErrUnavailable, err)
	}
	out := infos[:0]
	for _, info := range infos {
		if a.allowSession(authz.ActionRead, sessionFactsFor(info, a.authority)) {
			out = append(out, info)
		}
	}
	return out, nil
}

type ListPage struct {
	Sessions   []agentsession.Info
	NextOffset int
	HasMore    bool
}

// ListPage filters candidates through policy while retaining a cursor into the
// unfiltered durable result set. The next page therefore cannot be stranded by
// denied rows that happened to occupy the SQL page.
func (a *Access) ListPage(ctx context.Context, agentID string, opts agentsession.ListOptions, limit int) (ListPage, error) {
	if limit <= 0 {
		return ListPage{}, ErrForbidden
	}
	if !a.allowSessionList() {
		return ListPage{}, ErrNotFound
	}
	if err := a.allowSessionListAgent(agentID); err != nil {
		return ListPage{}, err
	}
	userID := string(a.authority.UserID())
	if userID == "" {
		return ListPage{}, ErrForbidden
	}

	kindAllowed := func(info agentsession.Info) bool {
		if len(opts.Kinds) == 0 {
			return true
		}
		return slices.Contains(opts.Kinds, agentsession.Kind(info.Kind))
	}
	batchSize := limit + 1
	offset := opts.Offset
	out := make([]agentsession.Info, 0, limit)
	for {
		query := opts
		query.Kinds = nil // filter here so the durable cursor counts every candidate
		query.Offset = offset
		query.Limit = batchSize
		var candidates []agentsession.Info
		var err error
		if a.authority.IsAdmin() {
			candidates, err = a.svc.registry.ListForAdmin(ctx, userID, agentID, query)
		} else {
			candidates, err = a.svc.registry.List(ctx, agentsession.Scope{UserID: userID, AgentID: agentID}, query)
		}
		if err != nil {
			return ListPage{}, fmt.Errorf("%w: list sessions: %w", ErrUnavailable, err)
		}
		for i, info := range candidates {
			if !kindAllowed(info) {
				continue
			}
			if !a.allowSession(authz.ActionRead, sessionFactsFor(info, a.authority)) {
				continue
			}
			if len(out) == limit {
				return ListPage{Sessions: out, NextOffset: offset + i, HasMore: true}, nil
			}
			out = append(out, info)
		}
		offset += len(candidates)
		if len(candidates) < batchSize {
			return ListPage{Sessions: out}, nil
		}
	}
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
	if a.authority.Kind() == authz.ActorGuest {
		guestID := string(a.authority.GuestID())
		if guestID == "" || req.UserID != guestID {
			return agentsession.Info{}, ErrNotFound
		}
		ctx = authz.WithGuestID(ctx, guestID)
	}
	if req.CreateIfMissing {
		if req.UserID == "" || req.Kind == "" || req.Channel == "" {
			return agentsession.Info{}, ErrForbidden
		}
		if err := a.authorizeAgent(ctx, req.AgentID, agentAction(action)); err != nil {
			return agentsession.Info{}, err
		}
		actor := a.authority
		facts := sessionFacts{
			isOwner:     string(actor.UserID()) != "" && string(actor.UserID()) == req.UserID,
			isExecutor:  string(actor.AgentID()) != "" && string(actor.AgentID()) == req.AgentID,
			isGroup:     req.GroupID != "",
			isSameGroup: req.GroupID != "" && string(actor.GroupID()) == req.GroupID,
		}
		if !a.allowSession(authz.ActionCreate, facts) {
			return agentsession.Info{}, ErrNotFound
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
	if !a.allowSession(action, sessionFactsFor(info, a.authority)) {
		return agentsession.Info{}, ErrNotFound
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
	loadCtx := authz.WithAgentID(ctx, conv.AgentID.String)
	if conv.GuestID.Valid {
		loadCtx = authz.WithGuestID(loadCtx, conv.GuestID.String)
	} else {
		loadCtx = authz.WithUserID(loadCtx, conv.UserID.String)
	}
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

// authorizeAgent delegates the session flow's Agent gate to the Agent PEP, so
// Agent scope/assignment/executor rules live in exactly one place. It reads for
// non-execute session ops and requires execute for a turn. A dedicated-channel
// Authority is honored through the Agent PEP's own channel-binding interpretation
// rather than any Agent rule duplicated here. Any Agent denial is folded into the
// opaque ErrNotFound so an otherwise-visible agent cannot reveal a session.
func (a *Access) authorizeAgent(ctx context.Context, agentID string, action authz.Action) error {
	switch err := a.svc.agents.Authorize(ctx, a.authority, agentID, action); {
	case err == nil:
		return nil
	case errors.Is(err, agentaccess.ErrForbidden), errors.Is(err, agentaccess.ErrNotFound):
		// The direct Agent rules denied; a dedicated channel binding may still
		// authorize this exact agent. The Agent PEP owns that interpretation.
		switch dErr := a.svc.agents.AuthorizeViaChannelBinding(ctx, a.authority, agentID); {
		case dErr == nil:
			return nil
		case errors.Is(dErr, agentaccess.ErrForbidden), errors.Is(dErr, agentaccess.ErrNotFound):
			return ErrNotFound
		default:
			return fmt.Errorf("%w: authorize agent: %w", ErrUnavailable, dErr)
		}
	default:
		return fmt.Errorf("%w: authorize agent: %w", ErrUnavailable, err)
	}
}

// sessionFacts are the four relationship bits the direct Session/Workspace rules
// compare. They are derived at the PEP from immutable authority plus durable
// conversation facts; a transport never supplies them.
type sessionFacts struct {
	isOwner        bool
	isGuestSession bool
	isSameGuest    bool
	isExecutor     bool
	isGroup        bool
	isSameGroup    bool
}

func sessionFactsFor(info agentsession.Info, authority authz.Authority) sessionFacts {
	actor := authority
	return sessionFacts{
		isOwner:        info.GuestID == "" && string(actor.UserID()) != "" && string(actor.UserID()) == info.UserID,
		isGuestSession: info.GuestID != "",
		isSameGuest:    info.GuestID != "" && string(actor.GuestID()) == info.GuestID,
		isExecutor:     string(actor.AgentID()) != "" && string(actor.AgentID()) == info.AgentID,
		isGroup:        info.GroupID != "",
		isSameGroup:    info.GroupID != "" && string(actor.GroupID()) == info.GroupID,
	}
}

// allowSessionList is the collection-level Session decision: admin and any
// user-role actor may list. A durable agent may list because List derives the
// owner and executor from its immutable Authority and still filters every row
// through the exact Session read rule. Group and system actors remain denied.
func (a *Access) allowSessionList() bool {
	return a.authority.IsAdmin() || a.authority.Kind() == authz.ActorUser || a.authority.Kind() == authz.ActorAgent
}

func (a *Access) allowSessionListAgent(agentID string) error {
	if a.authority.Kind() == authz.ActorAgent && string(a.authority.AgentID()) != agentID {
		return ErrNotFound
	}
	return nil
}

// allowSession decides one non-list action against a durable Session's facts.
// It reproduces the current builtin Session rules directly: a user owns every
// action on their sessions; a durable worker, a group turn, and a system worker
// may read/execute/create/delete — but never write — sessions matching their
// respective owner/executor, exact group/executor, or system actor.
func (a *Access) allowSession(action authz.Action, f sessionFacts) bool {
	if !isSessionAction(action) {
		return false
	}
	if f.isGuestSession || a.authority.Kind() == authz.ActorGuest {
		if a.authority.IsAdmin() && f.isGuestSession {
			return action == authz.ActionRead || action == authz.ActionDelete
		}
		return a.authority.Kind() == authz.ActorGuest && f.isGuestSession && f.isSameGuest && isWorkerSessionAction(action)
	}
	if a.authority.IsAdmin() {
		return true
	}
	switch a.authority.Kind() {
	case authz.ActorUser:
		return f.isOwner
	case authz.ActorAgent:
		return isWorkerSessionAction(action) && f.isOwner && f.isExecutor
	case authz.ActorGroupAgent:
		return isWorkerSessionAction(action) && f.isGroup && f.isSameGroup && f.isExecutor
	case authz.ActorSystem:
		return isWorkerSessionAction(action)
	default:
		return false
	}
}

// isSessionAction is the set of actions the Session rules recognize; anything
// else fails closed. Write is included because a user may write their own.
func isSessionAction(action authz.Action) bool {
	switch action {
	case authz.ActionRead, authz.ActionWrite, authz.ActionDelete, authz.ActionExecute, authz.ActionCreate:
		return true
	default:
		return false
	}
}

// isWorkerSessionAction is the narrower set a non-user actor (worker, group,
// system) may take on a session. It deliberately omits Write.
func isWorkerSessionAction(action authz.Action) bool {
	switch action {
	case authz.ActionRead, authz.ActionExecute, authz.ActionCreate, authz.ActionDelete:
		return true
	default:
		return false
	}
}

// allowWorkspace decides a Workspace action. Only an owning user (or admin) may
// touch the workspace rooted by a session; worker/group/system actors that can
// use a session still get no workspace widening.
func (a *Access) allowWorkspace(action authz.Action, f sessionFacts) bool {
	switch action {
	case authz.ActionRead, authz.ActionList, authz.ActionCreate, authz.ActionWrite, authz.ActionDelete:
	default:
		return false
	}
	if f.isGuestSession {
		return false
	}
	if a.authority.IsAdmin() {
		return true
	}
	return a.authority.Kind() == authz.ActorUser && f.isOwner
}
