package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/agentctx"
	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ErrGroupCompactionUnsupported is returned by CompactSession for a group
// session. Group history lives in the group event log, not the LCM conversation,
// so LCM compaction does not apply; callers surface this as a user-facing notice
// rather than silently compacting an event-log conversation.
var ErrGroupCompactionUnsupported = errors.New("compaction is not supported for group sessions")

// Service is a thin composition facade over session.Registry and runtime.Runtime.
// It provides ergonomic entry points for common use cases without hiding the
// conceptual split: policy lives in Session, execution lives in Runtime.
//
// Runtime owns execution; SessionAccess owns every lifecycle and policy
// decision. Sessions remains only as a legacy test fixture field and must not
// become a production entry point again.
type SessionAccessService interface {
	Begin(context.Context, authz.Authority) (SessionAccess, error)
}

type SessionAccess interface {
	Create(context.Context, string, string, string, session.Kind, session.Channel) (session.Info, error)
	ResolveMain(context.Context, string, string) (session.Info, error)
	RotateMain(context.Context, string, string, string) (session.Info, error)
	ResolveChatChannel(context.Context, session.ChannelRequest) (session.Info, error)
	RotateChannel(context.Context, session.ChannelRequest) (session.Info, error)
	Use(context.Context, string, string) (session.Info, error)
	EnsureRead(context.Context, session.Request) (session.Info, error)
	EnsureUse(context.Context, session.Request) (session.Info, error)
	Delete(context.Context, string, string) (session.Info, error)
	Archive(context.Context, session.Info) error
}

type Service struct {
	Sessions      *session.Registry // legacy fallback for tests only; production lifecycle goes through SessionAccess.
	Runtime       *agentruntime.Runtime
	SessionAccess SessionAccessService
	// AgentID is the executor this service belongs to.
	AgentID string
}

// ChatRequest describes a foreground chat turn.
type ChatRequest struct {
	SessionID string
	UserID    string
	AgentID   string
	ProjectID string
	Channel   session.Channel
	// Kind overrides the default session kind (KindChat). Used by non-chat
	// callers such as the scheduler (KindScheduler).
	Kind    session.Kind
	GroupID string // non-empty for group sessions; overlaid onto session.Info after Ensure
	GuestID string // durable channel_guest UUID; runtime restriction plumbing
	Message MessageContent
	Model   string
	// CurrentSpeaker is the human speaking this group turn. Personalization
	// target only (D9): forwarded to the runtime as WithCurrentSpeaker, never
	// used as the session/runtime UserID. Zero value for DM turns.
	CurrentSpeaker memory.CurrentSpeaker
	// RuntimeOpts are forwarded verbatim to Runtime.Chat.
	RuntimeOpts []agentruntime.Option
	// Authority is the trusted capability for resolving/using this session. It is
	// minted by the entry adapter from persisted identity, never from model text.
	Authority authz.Authority
}

// DelegateRequest describes a delegate session turn.
type DelegateRequest struct {
	// SessionID, when non-empty, resumes an existing delegate session.
	// When empty, a new delegate session is created.
	SessionID     string
	UserID        string
	AgentID       string
	ProjectID     string
	Task          string
	System        string
	Model         string
	ExcludedTools []string
	Authority     authz.Authority
}

// DelegateResult is the output of a delegate turn.
type DelegateResult struct {
	SessionID string
	Output    string
	Complete  bool
}

// ServiceManager provides multi-agent Service lookup.
// It replaces PoolManager for callers migrated to the new model.
type ServiceManager interface {
	// GetService returns the Service for the given agent ID, or nil if not found.
	GetService(agentID string) *Service
	// Default returns any service (first found). Useful for single-agent deployments.
	Default() *Service
}

// ChatAdmitted resolves (or creates) a session and synchronously admits one
// chat turn. A non-nil error means session access, session resolution, or the
// runtime busy guard rejected the turn before any run/session/tool side effect
// began. Once it returns a stream, the turn is admitted and all later runtime
// failures arrive on the stream — the call returns at admission, not completion.
func (s *Service) ChatAdmitted(ctx context.Context, req ChatRequest) (<-chan Event, error) {
	kind := req.Kind
	if kind == "" {
		kind = session.KindChat
	}
	ensureReq := session.Request{
		ID:        req.SessionID,
		UserID:    req.UserID,
		AgentID:   req.AgentID,
		GroupID:   req.GroupID,
		ProjectID: req.ProjectID,
		Kind:      kind,
		Channel:   req.Channel,
	}
	if req.SessionID != "" {
		if req.Kind != "" {
			ensureReq.RequireKind = kind
		}
	} else {
		ensureReq.CreateIfMissing = true
	}
	access, err := s.beginSessionAccess(ctx, req.Authority)
	if err != nil {
		return nil, fmt.Errorf("begin session access: %w", err)
	}
	info, err := access.EnsureUse(ctx, ensureReq)
	if err != nil {
		return nil, fmt.Errorf("resolve session: %w", err)
	}

	opts := req.RuntimeOpts
	if req.Model != "" {
		opts = append(opts, agentruntime.WithModel(req.Model))
	}
	if info.GroupID != "" && req.CurrentSpeaker != (memory.CurrentSpeaker{}) {
		opts = append(opts, agentruntime.WithCurrentSpeaker(req.CurrentSpeaker))
	}
	return s.Runtime.ChatAdmitted(ctx, info, req.Message, opts...)
}

// Chat resolves (or creates) a session and executes a chat turn.
//
// When SessionID is empty, a new session is created with a generated ID.
// When SessionID is non-empty, the session must already exist (resume-only);
// unknown IDs return an error instead of silently creating. A rejected admission
// remains its historic immediate error event; callers needing that distinction
// use ChatAdmitted.
func (s *Service) Chat(ctx context.Context, req ChatRequest) <-chan Event {
	stream, err := s.ChatAdmitted(ctx, req)
	if err != nil {
		return errorEvents(err)
	}
	return stream
}

// SubscribeSession registers a read-only listener for a session's live turn
// events, regardless of who initiated the turn. Used by the SSE endpoint to let
// the web UI watch scheduler/task/delegate turns in real time.
func (s *Service) SubscribeSession(sessionID string) (<-chan Event, func()) {
	return s.Runtime.Subscribe(sessionID)
}

// SessionLive reports whether a turn is currently in flight on the session.
func (s *Service) SessionLive(sessionID string) bool {
	return s.Runtime.SessionLive(sessionID)
}

// SchedulerChatRequest describes a scheduler-initiated chat turn.
type SchedulerChatRequest struct {
	SessionID string // scheduler-derived session ID
	UserID    string
	AgentID   string
	Message   MessageContent
	Model     string
	Authority authz.Authority
}

// ChatForScheduler resolves or creates a scheduler session using a trusted
// scheduler-derived session ID. Exact-ID creation is allowed because the
// scheduler system owns the ID derivation.
func (s *Service) ChatForScheduler(ctx context.Context, req SchedulerChatRequest) <-chan Event {
	access, err := s.beginSessionAccess(ctx, req.Authority)
	if err != nil {
		return errorEvents(fmt.Errorf("begin scheduler session access: %w", err))
	}
	info, err := access.EnsureUse(ctx, session.Request{
		ID:              req.SessionID,
		UserID:          req.UserID,
		AgentID:         req.AgentID,
		Kind:            session.KindScheduler,
		Channel:         session.ChannelScheduler,
		CreateIfMissing: true,
		// Scheduler-owned id, not a chat: no `/new`, so no channel binding.
		AllowExactIDCreate: true,
		RequireKind:        session.KindScheduler,
	})
	if err != nil {
		return errorEvents(fmt.Errorf("resolve scheduler session: %w", err))
	}

	var opts []agentruntime.Option
	if req.Model != "" {
		opts = append(opts, agentruntime.WithModel(req.Model))
	}
	return s.Runtime.Chat(ctx, info, req.Message, opts...)
}

// TaskChatRequest describes one worker turn on a durable task session.
type TaskChatRequest struct {
	SessionID        string // task session minted at task creation
	UserID           string
	AgentID          string
	ProjectID        string
	Message          MessageContent
	ExtraTools       []tools.Tool // per-run tools (e.g. task_control)
	ExcludedTools    []string
	OnSandboxSession func(sandbox.Session) error
	Authority        authz.Authority
}

// ChatForTask runs one persisted chat turn on a task session. Exact-ID
// creation is allowed because the task system owns the ID. The per-run extra
// tools force a fresh runner, which is evicted once the turn finishes so the
// tools never leak into later turns on the same session.
func (s *Service) ChatForTask(ctx context.Context, req TaskChatRequest) <-chan Event {
	return s.chatOnSession(ctx, session.Request{
		ID:              req.SessionID,
		UserID:          req.UserID,
		AgentID:         req.AgentID,
		ProjectID:       req.ProjectID,
		Kind:            session.KindTask,
		Channel:         session.ChannelTask,
		CreateIfMissing: true,
		// Task-owned id, not a chat: no `/new`, so no channel binding.
		AllowExactIDCreate: true,
		RequireKind:        session.KindTask,
	}, req)
}

// ChatForGoalDecomposition runs one persisted worker turn on a goal's
// decomposition planning session. Unlike a worker (execution) session that
// session is KindDelegate (#525: the plan session is resumable through the
// delegate tool and re-openable in the UI), so it must be resolved with
// RequireKind=KindDelegate — routing it through ChatForTask fails with a kind
// mismatch and starves the goal's decomposition budget. The session is pre-minted
// at BeginDecomposition, so this is resume-only and never creates.
func (s *Service) ChatForGoalDecomposition(ctx context.Context, req TaskChatRequest) <-chan Event {
	return s.chatOnSession(ctx, session.Request{
		ID:          req.SessionID,
		UserID:      req.UserID,
		AgentID:     req.AgentID,
		ProjectID:   req.ProjectID,
		Kind:        session.KindDelegate,
		Channel:     session.ChannelDelegate,
		RequireKind: session.KindDelegate,
	}, req)
}

// chatOnSession resolves the session described by sreq and runs one persisted
// worker turn on it with per-run extra tools, closing the session when the turn
// ends. The per-run tools force a fresh runner that is evicted once the turn
// finishes, so the tools never leak into later turns on the same session.
func (s *Service) chatOnSession(ctx context.Context, sreq session.Request, req TaskChatRequest) <-chan Event {
	access, err := s.beginSessionAccess(ctx, req.Authority)
	if err != nil {
		return errorEvents(fmt.Errorf("begin worker session access: %w", err))
	}
	info, err := access.EnsureUse(ctx, sreq)
	if err != nil {
		return errorEvents(fmt.Errorf("resolve worker session: %w", err))
	}

	opts := []agentruntime.Option{agentruntime.WithExtraTools(req.ExtraTools...)}
	if len(req.ExcludedTools) > 0 {
		opts = append(opts, agentruntime.WithExcludedTools(req.ExcludedTools...))
	}
	src := s.Runtime.Chat(ctx, info, req.Message, opts...)
	out := make(chan Event)
	go func() {
		defer close(out)
		for ev := range src {
			out <- ev
		}
		closeCtx := context.WithoutCancel(ctx)
		var err error
		if req.OnSandboxSession != nil {
			err = s.Runtime.CloseSessionWithSandbox(closeCtx, req.SessionID, req.OnSandboxSession)
		} else {
			err = s.Runtime.CloseSession(closeCtx, req.SessionID)
		}
		if err != nil {
			out <- Event{Err: fmt.Errorf("close worker session: %w", err)}
		}
	}()
	return out
}

// ResolvePrivateChannelSession resolves or creates a chat session whose id IS
// the caller's trusted system-derived key.
//
// Webhook ingress is the only remaining caller: a persistent webhook addresses
// exactly one durable session per webhook id, has no `/new`, and its channel
// (ChannelWebhook) is deliberately not its key — so it cannot use the
// chat-channel binding, which would either miss it or merge every webhook of a
// user into one session. Chat adapters use ResolveChatChannelSession instead.
func (s *Service) ResolvePrivateChannelSession(ctx context.Context, authority authz.Authority, sessionKey, userID, agentID string, channel session.Channel) (session.Info, error) {
	access, err := s.beginSessionAccess(ctx, authority)
	if err != nil {
		return session.Info{}, err
	}
	return access.EnsureRead(ctx, session.Request{
		ID:      sessionKey,
		UserID:  userID,
		AgentID: agentID,
		Kind:    session.KindChat,
		Channel: channel,
		// Webhook-only: the webhook system owns the key derivation, so the key may
		// become the session id. No chat adapter reaches this path.
		CreateIfMissing:    true,
		AllowExactIDCreate: true,
		RequireKind:        session.KindChat,
	})
}

// ChatChannelRequest identifies the durable binding of a channel chat: the chat
// itself rather than one of the sessions it has run through. A group binds on
// its owning group, a private chat channel on user + channel; either way the
// binding survives `/new` rotating the chat onto a fresh session.
type ChatChannelRequest struct {
	Authority authz.Authority
	// UserID owns the session. For a group chat the caller may leave it empty:
	// the group-ownership invariant (UserID == GroupID) is established here.
	UserID  string
	GroupID string
	// GuestID is the durable channel_guest UUID. When set it owns the session.
	GuestID string
	AgentID string
	Channel session.Channel
	// SessionKey is the chat's derived key. Sessions are no longer created under
	// it; it is kept as the legacy lookup for chats pinned to it before the
	// binding existed.
	SessionKey string
}

func (r ChatChannelRequest) binding() session.ChannelRequest {
	userID := r.UserID
	if r.GroupID != "" {
		userID = r.GroupID
	} else if r.GuestID != "" {
		userID = r.GuestID
	}
	return session.ChannelRequest{
		UserID:   userID,
		AgentID:  r.AgentID,
		GroupID:  r.GroupID,
		GuestID:  r.GuestID,
		Channel:  r.Channel,
		LegacyID: r.SessionKey,
	}
}

// ResolveChatChannelSession resolves the session a channel chat is currently
// bound to, creating one when the chat is new. It returns the Info without
// executing a chat turn.
func (s *Service) ResolveChatChannelSession(ctx context.Context, req ChatChannelRequest) (session.Info, error) {
	access, err := s.beginSessionAccess(ctx, req.Authority)
	if err != nil {
		return session.Info{}, err
	}
	return access.ResolveChatChannel(ctx, req.binding())
}

// ResolveChatChannelSessionForUse is ResolveChatChannelSession plus the fresh
// execute decision a turn (or a compaction) needs on the exact durable session.
func (s *Service) ResolveChatChannelSessionForUse(ctx context.Context, req ChatChannelRequest) (session.Info, error) {
	access, err := s.beginSessionAccess(ctx, req.Authority)
	if err != nil {
		return session.Info{}, err
	}
	info, err := access.ResolveChatChannel(ctx, req.binding())
	if err != nil {
		return session.Info{}, err
	}
	return access.Use(ctx, info.AgentID, info.ID)
}

// RotateChatChannelSession archives the chat's current session and returns the
// successor, which starts empty. It is the `/new` counterpart to
// ResolveChatChannelSessionForUse: rotation is one authorized compare-and-rotate
// use case, so a command that raced another rotation cannot archive the
// successor that one just created.
//
// expectedSessionID is the session the caller observed; an empty value rotates
// whatever is current. A mismatch reports session.ErrStaleRotation.
func (s *Service) RotateChatChannelSession(ctx context.Context, req ChatChannelRequest, expectedSessionID string) (session.Info, error) {
	access, err := s.beginSessionAccess(ctx, req.Authority)
	if err != nil {
		return session.Info{}, err
	}
	binding := req.binding()
	binding.ExpectedSessionID = expectedSessionID
	return access.RotateChannel(ctx, binding)
}

// NewSession creates a new session with a generated ID. Used by the HTTP API
// when the web UI creates a session — always new, never resume.
func (s *Service) NewSession(ctx context.Context, authority authz.Authority, userID, agentID, projectID string, kind session.Kind, channel session.Channel) (session.Info, error) {
	access, err := s.beginSessionAccess(ctx, authority)
	if err != nil {
		return session.Info{}, err
	}
	return access.Create(ctx, userID, agentID, projectID, kind, channel)
}

// MintTaskSession creates a new task worker session under the resolved agent.
// The session is always new (generated ID) and uses KindTask/ChannelTask.
func (s *Service) MintTaskSession(ctx context.Context, authority authz.Authority, userID, executorAgentID, projectID string) (session.Info, error) {
	access, err := s.beginSessionAccess(ctx, authority)
	if err != nil {
		return session.Info{}, err
	}
	return access.Create(ctx, userID, executorAgentID, projectID, session.KindTask, session.ChannelTask)
}

// Delegate runs a delegate turn through a persisted child session.
//
// When SessionID is empty, a new delegate session is created with a generated ID.
// When SessionID is non-empty, the session must already exist (resume-only);
// this prevents model-supplied session_id from reserving arbitrary future IDs.
func (s *Service) Delegate(ctx context.Context, req DelegateRequest) (DelegateResult, error) {
	if s.SessionAccess == nil || req.UserID == "" || req.AgentID == "" || req.AgentID != s.AgentID ||
		authz.UserIDFromContext(ctx) != req.UserID || authz.AgentIDFromContext(ctx) != s.AgentID ||
		(req.ProjectID != "" && req.ProjectID != memory.ProjectIDFromContext(ctx)) {
		return DelegateResult{SessionID: req.SessionID}, agentaccess.ErrForbidden
	}
	// A delegate turn runs in its own session, never in the chat that spawned it,
	// so it must not inherit that chat's binding — the authority that addresses
	// that chat's live session. The delegate tool clears this at
	// its own boundary too; this is the chokepoint every delegate turn passes.
	ctx = agentctx.WithoutChatBinding(ctx)
	authority := req.Authority
	if !authority.Valid() {
		var err error
		authority, err = agentaccess.WorkerAgentAuthority(req.UserID, s.AgentID)
		if err != nil {
			return DelegateResult{SessionID: req.SessionID}, agentaccess.ErrForbidden
		}
	}
	access, err := s.beginSessionAccess(ctx, authority)
	if err != nil {
		return DelegateResult{SessionID: req.SessionID}, fmt.Errorf("begin delegate session access: %w", err)
	}
	info, err := access.EnsureUse(ctx, session.Request{
		ID:              req.SessionID,
		UserID:          req.UserID,
		AgentID:         req.AgentID,
		ProjectID:       req.ProjectID,
		Kind:            session.KindDelegate,
		Channel:         session.ChannelDelegate,
		CreateIfMissing: req.SessionID == "",
		RequireKind:     session.KindDelegate,
	})
	if err != nil {
		return DelegateResult{SessionID: req.SessionID}, fmt.Errorf("ensure delegate session: %w", err)
	}
	if info.UserID != req.UserID || info.AgentID != s.AgentID || info.GroupID != "" || info.ProjectID != req.ProjectID {
		return DelegateResult{SessionID: info.ID}, agentaccess.ErrForbidden
	}
	// access.EnsureUse above already decided both the persisted Session and its
	// backing Agent under this use case's single policy evaluation. Starting an
	// AgentAccess evaluation here would create a revocation race and violate the
	// one-Begin Session vertical contract.

	opts := []agentruntime.Option{}
	if req.Model != "" {
		opts = append(opts, agentruntime.WithModel(req.Model))
	}
	if req.System != "" {
		opts = append(opts, agentruntime.WithSystemOverride(req.System))
	}
	if len(req.ExcludedTools) > 0 {
		opts = append(opts, agentruntime.WithExcludedTools(req.ExcludedTools...))
	}

	stream := s.Runtime.Chat(ctx, info, req.Task, opts...)
	result := DelegateResult{SessionID: info.ID}
	var output strings.Builder
	for ev := range stream {
		if ev.Text != "" {
			output.WriteString(ev.Text)
		}
		if ev.Err != nil {
			return DelegateResult{SessionID: info.ID, Output: output.String()}, ev.Err
		}
	}
	result.Output = output.String()
	result.Complete = true
	return result, nil
}

// RunDelegateSession implements delegatetool.SessionRunner so that Service can
// be passed directly to delegate tool constructors. Its identity is the parent
// runtime's validated session identity: a delegate tool cannot choose an owner,
// executor, or project through its model arguments.
func (s *Service) RunDelegateSession(ctx context.Context, req delegatetool.SessionRunRequest) (delegatetool.SessionRunResult, error) {
	userID := authz.UserIDFromContext(ctx)
	agentID := authz.AgentIDFromContext(ctx)
	if userID == "" || agentID == "" || agentID != s.AgentID {
		return delegatetool.SessionRunResult{SessionID: req.SessionID}, agentaccess.ErrForbidden
	}
	projectID := memory.ProjectIDFromContext(ctx)
	if req.ProjectID != "" && req.ProjectID != projectID {
		return delegatetool.SessionRunResult{SessionID: req.SessionID}, agentaccess.ErrForbidden
	}
	authority, err := agentaccess.WorkerAgentAuthority(userID, agentID)
	if err != nil {
		return delegatetool.SessionRunResult{SessionID: req.SessionID}, agentaccess.ErrForbidden
	}
	res, err := s.Delegate(ctx, DelegateRequest{
		SessionID:     req.SessionID,
		UserID:        userID,
		AgentID:       agentID,
		ProjectID:     projectID,
		Task:          req.Task,
		System:        req.System,
		Model:         req.Model,
		ExcludedTools: req.ExcludedTools,
		Authority:     authority,
	})
	return delegatetool.SessionRunResult{
		SessionID: res.SessionID,
		Output:    res.Output,
		Complete:  res.Complete,
	}, err
}

func (s *Service) ArchiveSession(ctx context.Context, authority authz.Authority, userID, agentID, sessionID string) error {
	access, err := s.beginSessionAccess(ctx, authority)
	if err != nil {
		return err
	}
	info, err := access.Delete(ctx, agentID, sessionID)
	if err != nil {
		return err
	}
	if info.UserID != userID || info.AgentID != agentID {
		return agentaccess.ErrForbidden
	}
	return access.Archive(ctx, info)
}

// ResolveMainSession resolves the main session for a user+agent pair, creating
// one if missing. It is the canonical replacement for Pool.ResolveSession on
// private user channels.
func (s *Service) ResolveMainSession(ctx context.Context, authority authz.Authority, userID, agentID string) (session.Info, error) {
	return s.resolveMainSession(ctx, authority, userID, agentID, false)
}

func (s *Service) ResolveMainSessionForUse(ctx context.Context, authority authz.Authority, userID, agentID string) (session.Info, error) {
	return s.resolveMainSession(ctx, authority, userID, agentID, true)
}

func (s *Service) resolveMainSession(ctx context.Context, authority authz.Authority, userID, agentID string, use bool) (session.Info, error) {
	if agentID == "" {
		agentID = s.AgentID
	}
	access, err := s.beginSessionAccess(ctx, authority)
	if err != nil {
		return session.Info{}, err
	}
	info, err := access.ResolveMain(ctx, userID, agentID)
	if err != nil || !use {
		return info, err
	}
	return access.Use(ctx, info.AgentID, info.ID)
}

// RotateMainSession archives the user's current main session and returns the
// successor, which starts empty. It is the `/new` counterpart to
// ResolveMainSessionForUse: rotation is one authorized compare-and-rotate use
// case rather than a resolve followed by a separate archive, so a turn that
// raced another rotation cannot archive the successor.
//
// expectedSessionID is the main session the caller observed; an empty value
// rotates whatever is current. A mismatch reports session.ErrStaleRotation.
func (s *Service) RotateMainSession(ctx context.Context, authority authz.Authority, userID, agentID, expectedSessionID string) (session.Info, error) {
	if agentID == "" {
		agentID = s.AgentID
	}
	access, err := s.beginSessionAccess(ctx, authority)
	if err != nil {
		return session.Info{}, err
	}
	return access.RotateMain(ctx, userID, agentID, expectedSessionID)
}

// CompactSession runs full compaction on the session identified by sessionID.
// This is a best-effort operation: it returns the compaction summary or an error.
//
// Group sessions are unsupported: their history is assembled from the group event
// log, not the LCM conversation, and NeedsCompaction already declines them. The
// manual path rejects a group session up front (before touching the compactor)
// with ErrGroupCompactionUnsupported rather than run a private-style compaction
// over an event-log conversation.
// CompactAuthorizedSession performs compaction after the caller has authorized
// ActionExecute for info under its current Session Access evaluation.
func (s *Service) CompactAuthorizedSession(ctx context.Context, info session.Info) (string, error) {
	if info.GroupID != "" {
		return "", ErrGroupCompactionUnsupported
	}
	if info.GuestID != "" {
		ctx = authz.WithGuestID(ctx, info.GuestID)
	} else {
		ctx = authz.WithUserID(ctx, info.UserID)
	}
	ctx = authz.WithAgentID(ctx, info.AgentID)
	mem := s.Runtime.Memory()
	if mem == nil {
		return "", fmt.Errorf("no memory provider")
	}
	c, ok := mem.(interface {
		Compact(ctx context.Context, session memory.Session, mode memory.CompactionMode) (*memory.CompactionResult, error)
	})
	if !ok {
		return "", fmt.Errorf("memory provider does not support compaction")
	}
	memSess, err := s.Sessions.MemoryScope(info)
	if err != nil {
		return "", err
	}
	result, err := c.Compact(ctx, memSess, memory.CompactionFull)
	if err != nil {
		return "", fmt.Errorf("compact: %w", err)
	}
	return fmt.Sprintf("compacted: %d leaf + %d condensed summaries, %d→%d tokens",
		result.LeafSummariesCreated, result.CondensedSummariesCreated,
		result.TokensBefore, result.TokensAfter), nil
}

// History returns the raw message history for the given session.
func (s *Service) beginSessionAccess(ctx context.Context, authority authz.Authority) (SessionAccess, error) {
	if s.SessionAccess == nil {
		return nil, fmt.Errorf("session access is not bound")
	}
	return s.SessionAccess.Begin(ctx, authority)
}

func errorEvents(err error) <-chan Event {
	out := make(chan Event, 1)
	out <- Event{Err: err}
	close(out)
	return out
}

func (s *Service) History(ctx context.Context, info session.Info) []ai.Message {
	mem := s.Runtime.Memory()
	if mem == nil {
		return nil
	}
	sm, ok := mem.(memory.SessionManager)
	if !ok {
		return nil
	}
	saveCtx := ctx
	if info.GuestID != "" {
		saveCtx = authz.WithGuestID(saveCtx, info.GuestID)
	} else {
		saveCtx = authz.WithUserID(saveCtx, info.UserID)
	}
	saveCtx = authz.WithAgentID(saveCtx, info.AgentID)
	msgs, err := sm.LoadHistory(saveCtx, info.ID)
	if err != nil {
		return nil
	}
	return msgs
}
