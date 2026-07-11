package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
// Callers that need fine-grained control can use Sessions and Runtime directly.
type Service struct {
	Sessions *session.Registry
	Runtime  *agentruntime.Runtime
	// AgentID is the agent this service belongs to.
	// Used by RunDelegateSession when the caller does not supply an agent ID.
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
	Message MessageContent
	Model   string
	// CurrentSpeaker is the human speaking this group turn. Personalization
	// target only (D9): forwarded to the runtime as WithCurrentSpeaker, never
	// used as the session/runtime UserID. Zero value for DM turns.
	CurrentSpeaker memory.CurrentSpeaker
	// RuntimeOpts are forwarded verbatim to Runtime.Chat.
	RuntimeOpts []agentruntime.Option
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

// Chat resolves (or creates) a session and executes a chat turn.
//
// When SessionID is empty, a new session is created with a generated ID.
// When SessionID is non-empty, the session must already exist (resume-only);
// unknown IDs return an error instead of silently creating.
func (s *Service) Chat(ctx context.Context, req ChatRequest) <-chan Event {
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
		// Resume: enforce the caller-declared kind matches the stored session
		// (any kind, including internal delegate/task/scheduler sessions).
		if req.Kind != "" {
			ensureReq.RequireKind = kind
		}
	} else {
		ensureReq.CreateIfMissing = true
	}
	info, err := s.Sessions.Ensure(ctx, ensureReq)
	if err != nil {
		out := make(chan Event, 1)
		out <- Event{Err: fmt.Errorf("resolve session: %w", err)}
		close(out)
		return out
	}
	// GroupID is threaded through the Request and owned by the Registry (create
	// persists it; resume overlays it), so info.GroupID is already set here.

	opts := req.RuntimeOpts
	if req.Model != "" {
		opts = append(opts, agentruntime.WithModel(req.Model))
	}
	if info.GroupID != "" && req.CurrentSpeaker != (memory.CurrentSpeaker{}) {
		opts = append(opts, agentruntime.WithCurrentSpeaker(req.CurrentSpeaker))
	}
	return s.Runtime.Chat(ctx, info, req.Message, opts...)
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

// ChannelChatRequest describes a chat turn on a non-private channel/group session
// identified by a Stella-derived session key.
type ChannelChatRequest struct {
	SessionKey string // system-derived session key (e.g. "agent:telegram:group:123")
	UserID     string
	AgentID    string
	Channel    session.Channel
	Message    MessageContent
	Model      string
}

// ChatForChannel resolves or creates a channel/group chat session using a
// trusted system-derived session key. Unlike Chat, this method allows exact-ID
// creation because the key is derived by Stella, not by user/model input.
func (s *Service) ChatForChannel(ctx context.Context, req ChannelChatRequest) <-chan Event {
	info, err := s.Sessions.Ensure(ctx, session.Request{
		ID:                 req.SessionKey,
		UserID:             req.UserID,
		AgentID:            req.AgentID,
		Kind:               session.KindChat,
		Channel:            req.Channel,
		CreateIfMissing:    true,
		AllowExactIDCreate: true,
		RequireKind:        session.KindChat,
	})
	if err != nil {
		out := make(chan Event, 1)
		out <- Event{Err: fmt.Errorf("resolve channel session: %w", err)}
		close(out)
		return out
	}

	var opts []agentruntime.Option
	if req.Model != "" {
		opts = append(opts, agentruntime.WithModel(req.Model))
	}
	return s.Runtime.Chat(ctx, info, req.Message, opts...)
}

// SchedulerChatRequest describes a scheduler-initiated chat turn.
type SchedulerChatRequest struct {
	SessionID string // scheduler-derived session ID
	UserID    string
	AgentID   string
	Message   MessageContent
	Model     string
}

// ChatForScheduler resolves or creates a scheduler session using a trusted
// scheduler-derived session ID. Exact-ID creation is allowed because the
// scheduler system owns the ID derivation.
func (s *Service) ChatForScheduler(ctx context.Context, req SchedulerChatRequest) <-chan Event {
	info, err := s.Sessions.Ensure(ctx, session.Request{
		ID:                 req.SessionID,
		UserID:             req.UserID,
		AgentID:            req.AgentID,
		Kind:               session.KindScheduler,
		Channel:            session.ChannelScheduler,
		CreateIfMissing:    true,
		AllowExactIDCreate: true,
		RequireKind:        session.KindScheduler,
	})
	if err != nil {
		out := make(chan Event, 1)
		out <- Event{Err: fmt.Errorf("resolve scheduler session: %w", err)}
		close(out)
		return out
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
}

// ChatForTask runs one persisted chat turn on a task session. Exact-ID
// creation is allowed because the task system owns the ID. The per-run extra
// tools force a fresh runner, which is evicted once the turn finishes so the
// tools never leak into later turns on the same session.
func (s *Service) ChatForTask(ctx context.Context, req TaskChatRequest) <-chan Event {
	return s.chatOnSession(ctx, session.Request{
		ID:                 req.SessionID,
		UserID:             req.UserID,
		AgentID:            req.AgentID,
		ProjectID:          req.ProjectID,
		Kind:               session.KindTask,
		Channel:            session.ChannelTask,
		CreateIfMissing:    true,
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
	info, err := s.Sessions.Ensure(ctx, sreq)
	if err != nil {
		out := make(chan Event, 1)
		out <- Event{Err: fmt.Errorf("resolve worker session: %w", err)}
		close(out)
		return out
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

// ResolvePrivateChannelSession resolves or creates a private channel chat session
// using a trusted system-derived session key. It is the session-only variant of
// ChatForChannel for a per-user channel; it returns the Info without executing a
// chat turn. Private callers do not know about groups.
func (s *Service) ResolvePrivateChannelSession(ctx context.Context, sessionKey, userID, agentID string, channel session.Channel) (session.Info, error) {
	return s.Sessions.Ensure(ctx, session.Request{
		ID:                 sessionKey,
		UserID:             userID,
		AgentID:            agentID,
		Kind:               session.KindChat,
		Channel:            channel,
		CreateIfMissing:    true,
		AllowExactIDCreate: true,
		RequireKind:        session.KindChat,
	})
}

// ResolveGroupChannelSession resolves or creates a group chat session owned by
// the group. The caller passes the canonical group id once; the group-ownership
// invariant (a group session is owned by its group, so UserID == GroupID) is
// established here in one place rather than by callers repeating the id.
func (s *Service) ResolveGroupChannelSession(ctx context.Context, sessionKey, groupID, agentID string, channel session.Channel) (session.Info, error) {
	return s.Sessions.Ensure(ctx, session.Request{
		ID:                 sessionKey,
		UserID:             groupID,
		AgentID:            agentID,
		GroupID:            groupID,
		Kind:               session.KindChat,
		Channel:            channel,
		CreateIfMissing:    true,
		AllowExactIDCreate: true,
		RequireKind:        session.KindChat,
	})
}

// NewSession creates a new session with a generated ID. Used by the HTTP API
// when the web UI creates a session — always new, never resume.
func (s *Service) NewSession(ctx context.Context, userID, agentID, projectID string, kind session.Kind, channel session.Channel) (session.Info, error) {
	return s.Sessions.Ensure(ctx, session.Request{
		UserID:          userID,
		AgentID:         agentID,
		ProjectID:       projectID,
		Kind:            kind,
		Channel:         channel,
		CreateIfMissing: true,
	})
}

// MintTaskSession creates a new task worker session under the resolved agent.
// The session is always new (generated ID) and uses KindTask/ChannelTask.
func (s *Service) MintTaskSession(ctx context.Context, userID, executorAgentID, projectID string) (session.Info, error) {
	return s.Sessions.Ensure(ctx, session.Request{
		UserID:          userID,
		AgentID:         executorAgentID,
		ProjectID:       projectID,
		Kind:            session.KindTask,
		Channel:         session.ChannelTask,
		CreateIfMissing: true,
	})
}

// Delegate runs a delegate turn through a persisted child session.
//
// When SessionID is empty, a new delegate session is created with a generated ID.
// When SessionID is non-empty, the session must already exist (resume-only);
// this prevents model-supplied session_id from reserving arbitrary future IDs.
func (s *Service) Delegate(ctx context.Context, req DelegateRequest) (DelegateResult, error) {
	ensureReq := session.Request{
		ID:              req.SessionID,
		UserID:          req.UserID,
		AgentID:         req.AgentID,
		ProjectID:       req.ProjectID,
		Kind:            session.KindDelegate,
		Channel:         session.ChannelDelegate,
		CreateIfMissing: req.SessionID == "",
		RequireKind:     session.KindDelegate,
	}
	info, err := s.Sessions.Ensure(ctx, ensureReq)
	if err != nil {
		return DelegateResult{SessionID: req.SessionID}, fmt.Errorf("ensure delegate session: %w", err)
	}

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
// be passed directly to delegate tool constructors.
// UserID is resolved from ctx; AgentID falls back to s.AgentID.
func (s *Service) RunDelegateSession(ctx context.Context, req delegatetool.SessionRunRequest) (delegatetool.SessionRunResult, error) {
	userID := authz.UserIDFromContext(ctx)
	agentID := authz.AgentIDFromContext(ctx)
	if agentID == "" {
		agentID = s.AgentID
	}
	projectID := req.ProjectID
	if projectID == "" {
		projectID = memory.ProjectIDFromContext(ctx)
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
	})
	return delegatetool.SessionRunResult{
		SessionID: res.SessionID,
		Output:    res.Output,
		Complete:  res.Complete,
	}, err
}

// ResolveMainSession resolves the main session for a user+agent pair, creating
// one if missing. It is the canonical replacement for Pool.ResolveSession on
// private user channels.
func (s *Service) ResolveMainSession(ctx context.Context, userID, agentID string) (session.Info, error) {
	if agentID == "" {
		agentID = s.AgentID
	}
	return s.Sessions.ResolveMain(ctx, session.MainRequest{
		UserID:  userID,
		AgentID: agentID,
	})
}

// CompactSession runs full compaction on the session identified by sessionID.
// This is a best-effort operation: it returns the compaction summary or an error.
//
// Group sessions are unsupported: their history is assembled from the group event
// log, not the LCM conversation, and NeedsCompaction already declines them. The
// manual path rejects a group session up front (before touching the compactor)
// with ErrGroupCompactionUnsupported rather than run a private-style compaction
// over an event-log conversation.
func (s *Service) CompactSession(ctx context.Context, info session.Info) (string, error) {
	if info.GroupID != "" {
		return "", ErrGroupCompactionUnsupported
	}
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
func (s *Service) History(ctx context.Context, info session.Info) []ai.Message {
	mem := s.Runtime.Memory()
	if mem == nil {
		return nil
	}
	sm, ok := mem.(memory.SessionManager)
	if !ok {
		return nil
	}
	saveCtx := authz.WithUserID(ctx, info.UserID)
	saveCtx = authz.WithAgentID(saveCtx, info.AgentID)
	msgs, err := sm.LoadHistory(saveCtx, info.ID)
	if err != nil {
		return nil
	}
	return msgs
}
