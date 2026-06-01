package agent

import (
	"context"
	"fmt"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	delegatetool "github.com/CherryHQ/stella/internal/tools/delegate"
	"github.com/CherryHQ/stella/pkg/ai"
)

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
	Message   MessageContent
	Model     string
	// RuntimeOpts are forwarded verbatim to Runtime.Chat.
	RuntimeOpts []agentruntime.Option
}

// DelegateRequest describes a delegate session turn.
type DelegateRequest struct {
	// SessionID, when non-empty, resumes an existing delegate session.
	// When empty, a new delegate session is created.
	SessionID       string
	UserID          string
	AgentID         string
	ParentSessionID string
	ProjectID       string
	Task            string
	System          string
	Model           string
	ExcludedTools   []string
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
// For private user channels, it calls Sessions.ResolveMain automatically.
func (s *Service) Chat(ctx context.Context, req ChatRequest) <-chan Event {
	info, err := s.Sessions.Ensure(ctx, session.Request{
		ID:              req.SessionID,
		UserID:          req.UserID,
		AgentID:         req.AgentID,
		ProjectID:       req.ProjectID,
		Kind:            session.KindChat,
		Channel:         req.Channel,
		CreateIfMissing: true,
	})
	if err != nil {
		out := make(chan Event, 1)
		out <- Event{Err: fmt.Errorf("resolve session: %w", err)}
		close(out)
		return out
	}

	opts := req.RuntimeOpts
	if req.Model != "" {
		opts = append(opts, agentruntime.WithModel(req.Model))
	}
	return s.Runtime.Chat(ctx, info, req.Message, opts...)
}

// Delegate runs a delegate turn through a persisted child session.
// The session is created if missing, or resumed if SessionID is set.
// Returns an error if the resolved session is not a delegate session.
func (s *Service) Delegate(ctx context.Context, req DelegateRequest) (DelegateResult, error) {
	ensureReq := session.Request{
		ID:                 req.SessionID,
		UserID:             req.UserID,
		AgentID:            req.AgentID,
		ProjectID:          req.ProjectID,
		ParentSessionID:    req.ParentSessionID,
		Kind:               session.KindDelegate,
		Channel:            session.ChannelDelegate,
		CreateIfMissing:    true,
		AllowExactIDCreate: req.SessionID != "",
		RequireKind:        session.KindDelegate,
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
	var sb fmt.Stringer
	_ = sb
	var output string
	for ev := range stream {
		if ev.Text != "" {
			output += ev.Text
		}
		if ev.Err != nil {
			return DelegateResult{SessionID: info.ID, Output: output}, ev.Err
		}
	}
	result.Output = output
	result.Complete = true
	return result, nil
}

// RunDelegateSession implements delegatetool.SessionRunner so that Service can
// be passed directly to delegate tool constructors.
// UserID is resolved from ctx; AgentID falls back to s.AgentID.
func (s *Service) RunDelegateSession(ctx context.Context, req delegatetool.SessionRunRequest) (delegatetool.SessionRunResult, error) {
	userID := memory.UserIDFromContext(ctx)
	agentID := memory.AgentIDFromContext(ctx)
	if agentID == "" {
		agentID = s.AgentID
	}
	res, err := s.Delegate(ctx, DelegateRequest{
		SessionID:     req.SessionID,
		UserID:        userID,
		AgentID:       agentID,
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
func (s *Service) CompactSession(ctx context.Context, info session.Info) (string, error) {
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
	memSess := s.Sessions.MemoryScope(info)
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
	saveCtx := memory.WithUserID(ctx, info.UserID)
	saveCtx = memory.WithAgentID(saveCtx, info.AgentID)
	msgs, err := sm.LoadHistory(saveCtx, info.ID)
	if err != nil {
		return nil
	}
	return msgs
}
