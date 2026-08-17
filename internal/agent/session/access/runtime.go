package access

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
)

// RuntimeManager is the typed runtime lookup port used by session Send/Attach.
// Tests can provide a deterministic in-memory implementation; production wraps
// the agent pool with NewRuntimeManager.
type RuntimeManager interface {
	GetService(agentID string) RuntimeService
	Default() RuntimeService
}

// AgentServiceManager is the production pool shape. The adapter keeps the
// Session access port narrow despite agent.PoolManager returning concrete
// services from its public API.
type AgentServiceManager interface {
	GetService(agentID string) *agent.Service
	Default() *agent.Service
}

type agentRuntimeManager struct{ inner AgentServiceManager }

func NewRuntimeManager(inner AgentServiceManager) RuntimeManager {
	return agentRuntimeManager{inner: inner}
}

// NewAgentSessionAccess adapts Service to agent.Service's narrow session PEP
// port without making internal/agent import Session access.
func NewAgentSessionAccess(svc *Service) agent.SessionAccessService {
	return agentSessionAccess{svc: svc}
}

type agentSessionAccess struct{ svc *Service }

func (a agentSessionAccess) Begin(ctx context.Context, authority authz.Authority) (agent.SessionAccess, error) {
	return a.svc.Begin(ctx, authority)
}

func (m agentRuntimeManager) GetService(agentID string) RuntimeService {
	if m.inner == nil {
		return nil
	}
	service := m.inner.GetService(agentID)
	if service == nil {
		return nil
	}
	return service
}

func (m agentRuntimeManager) Default() RuntimeService {
	if m.inner == nil {
		return nil
	}
	service := m.inner.Default()
	if service == nil {
		return nil
	}
	return service
}

// RuntimeService is the narrow live-turn port Session access needs from the
// agent runtime. It deliberately excludes session lookup and policy concerns.
type RuntimeService interface {
	Chat(context.Context, agent.ChatRequest) <-chan agent.Event
	RunManagedSession(context.Context, delegatetool.ManagedSessionRequest) (delegatetool.ManagedSessionResult, error)
	RunConversationSession(context.Context, agentsession.Info, agent.MessageContent) <-chan agent.Event
	StopSession(context.Context, string) bool
	SubscribeSession(sessionID string) (<-chan agent.Event, func())
	SessionLive(sessionID string) bool
	CompactAuthorizedSession(context.Context, agentsession.Info) (string, error)
}

// BindRuntimeManager wires the live runtime port after the pool has been
// constructed. It is a one-time composition-root bind; handlers must not look up
// agent services directly.
func (s *Service) BindRuntimeManager(runtime RuntimeManager) error {
	if runtime == nil {
		return fmt.Errorf("session access: missing runtime manager")
	}
	if s.runtime != nil {
		return fmt.Errorf("session access: runtime manager already bound")
	}
	s.runtime = runtime
	return nil
}

type SendInput struct {
	Authority authz.Authority
	AgentID   string
	SessionID string
	Message   agent.MessageContent
}

type SendResult struct {
	Events     <-chan agent.Event
	PlainReply string
}

// Send authorizes and starts exactly one foreground turn. The returned event
// chunks are not re-authorized by Session access; the single Access evaluation
// covers the turn initiation, matching the send semantics expected by the UI.
func (s *Service) Send(ctx, runCtx context.Context, in SendInput) (SendResult, error) {
	access, err := s.Begin(ctx, in.Authority)
	if err != nil {
		return SendResult{}, err
	}
	info, err := access.Use(ctx, in.AgentID, in.SessionID)
	if err != nil {
		return SendResult{}, err
	}
	runtime, err := s.runtimeFor(info.AgentID)
	if err != nil {
		return SendResult{}, err
	}
	if text, ok := in.Message.(string); ok {
		if reply, handled := handleCommand(ctx, runtime, info, text); handled {
			return SendResult{PlainReply: reply}, nil
		}
	}
	// Rotation can archive the session a client is holding (a `/new` from another
	// surface). The turn would fail deep inside the runtime and reach the client as
	// free text in the event stream, so reject it here while the caller can still be
	// told what happened.
	if info.Archived {
		return SendResult{}, fmt.Errorf("%w: %s", agentsession.ErrArchived, in.SessionID)
	}
	// Authorization and input resolution use the request context above. The
	// admitted turn uses the server lifecycle context so losing the initiating
	// HTTP/SSE connection only detaches that observer; it does not kill work.
	ch := runtime.Chat(runCtx, agent.ChatRequest{
		SessionID: in.SessionID,
		UserID:    info.UserID,
		AgentID:   info.AgentID,
		Kind:      agentsession.Kind(info.Kind),
		Channel:   agentsession.Channel(info.Channel),
		Message:   in.Message,
		Authority: in.Authority,
	})
	return SendResult{Events: relayEventsUntilDone(ctx, ch)}, nil
}

// observerStallCeiling separates transient HTTP backpressure from a peer that
// has stopped reading without closing its connection.
const observerStallCeiling = 30 * time.Second

// relayEventsUntilDone keeps draining the runtime's initiating stream after the
// HTTP observer disconnects. The runtime publishes to attach subscribers before
// writing this stream, but leaving it unread would eventually fill its bounded
// buffer and stall the producer.
func relayEventsUntilDone(observerCtx context.Context, source <-chan agent.Event) <-chan agent.Event {
	out := make(chan agent.Event, 100)
	go func() {
		defer close(out)
		stall := time.NewTimer(observerStallCeiling)
		defer stall.Stop()
		forward := true
		for event := range source {
			if !forward {
				continue
			}
			if !stall.Stop() {
				select {
				case <-stall.C:
				default:
				}
			}
			stall.Reset(observerStallCeiling)
			select {
			case out <- event:
			case <-observerCtx.Done():
				forward = false
			case <-stall.C:
				// A connection can remain open while its peer stops reading. Detach
				// only after sustained backpressure; reconnect and persisted history
				// are the recovery paths.
				forward = false
			}
		}
	}()
	return out
}

// SessionRunning reports process-local turn state for an already-authorized
// session record. Durable terminal state remains on Info.
func (a *Access) SessionRunning(info agentsession.Info) bool {
	runtime, err := a.svc.runtimeFor(info.AgentID)
	return err == nil && runtime.SessionLive(info.ID)
}

type MarkViewedInput struct {
	Authority authz.Authority
	AgentID   string
	SessionID string
}

// MarkViewed clears terminal status for a session the caller can read.
func (s *Service) MarkViewed(ctx context.Context, in MarkViewedInput) error {
	access, err := s.Begin(ctx, in.Authority)
	if err != nil {
		return err
	}
	info, err := access.Read(ctx, in.AgentID, in.SessionID)
	if err != nil {
		return err
	}
	activity, ok := s.memory.(memory.SessionActivityStore)
	if !ok {
		return fmt.Errorf("%w: session activity store unavailable", ErrUnavailable)
	}
	scope, err := info.MemoryScope()
	if err != nil {
		return fmt.Errorf("%w: invalid session scope: %w", ErrUnavailable, err)
	}
	updated, err := activity.MarkSessionViewed(ctx, scope)
	if err != nil {
		return fmt.Errorf("%w: mark session viewed: %w", ErrUnavailable, err)
	}
	if !updated {
		return ErrNotFound
	}
	return nil
}

type StopInput struct {
	Authority authz.Authority
	AgentID   string
	SessionID string
}

// Stop authorizes use of a session and explicitly cancels its active turn. It
// is idempotent: stopping an idle session succeeds without inventing state.
func (s *Service) Stop(ctx context.Context, in StopInput) error {
	access, err := s.Begin(ctx, in.Authority)
	if err != nil {
		return err
	}
	info, err := access.Use(ctx, in.AgentID, in.SessionID)
	if err != nil {
		return err
	}
	runtime, err := s.runtimeFor(info.AgentID)
	if err != nil {
		return err
	}
	runtime.StopSession(ctx, in.SessionID)
	return nil
}

type AttachInput struct {
	Authority authz.Authority
	AgentID   string
	SessionID string
}

type AttachResult struct {
	Events               <-chan agent.Event
	Cancel               func()
	Live                 bool
	RunID                string
	Remote               bool
	BeforeProtectedEvent func(context.Context) error
}

type durableRunRuntime interface {
	RunningRun(context.Context, string) (runID, executorBootID string, running bool, err error)
	ExecutorBootID() string
}

// Attach authorizes read access and subscribes to an existing live turn. It
// never starts a turn. The returned guard starts a fresh Access and Read
// immediately before each non-store event is encoded, so revocation cannot leak
// the protected source event that triggered the check.
func (s *Service) Attach(ctx context.Context, in AttachInput) (AttachResult, error) {
	access, err := s.Begin(ctx, in.Authority)
	if err != nil {
		return AttachResult{}, err
	}
	info, err := access.Read(ctx, in.AgentID, in.SessionID)
	if err != nil {
		return AttachResult{}, err
	}
	runtime, err := s.runtimeFor(info.AgentID)
	if err != nil {
		return AttachResult{}, err
	}
	ch, cancel := runtime.SubscribeSession(in.SessionID)
	live := runtime.SessionLive(in.SessionID)
	var runID string
	remote := false
	if durable, ok := runtime.(durableRunRuntime); ok {
		ownerBootID := ""
		var running bool
		runID, ownerBootID, running, err = durable.RunningRun(ctx, in.SessionID)
		if err != nil {
			cancel()
			return AttachResult{}, fmt.Errorf("lookup active AgentRun: %w", err)
		}
		if running && (!live || ownerBootID != durable.ExecutorBootID()) {
			remote = true
		}
	}
	return AttachResult{
		Events: ch,
		Cancel: cancel,
		Live:   live,
		RunID:  runID,
		Remote: remote,
		BeforeProtectedEvent: func(eventCtx context.Context) error {
			fresh, err := s.Begin(eventCtx, in.Authority)
			if err != nil {
				return err
			}
			_, err = fresh.Read(eventCtx, in.AgentID, in.SessionID)
			return err
		},
	}, nil
}

func (s *Service) runtimeFor(agentID string) (RuntimeService, error) {
	if s.runtime == nil {
		return nil, ErrUnavailable
	}
	if agentID != "" {
		if svc := s.runtime.GetService(agentID); svc != nil {
			return svc, nil
		}
	} else if svc := s.runtime.Default(); svc != nil {
		return svc, nil
	}
	return nil, ErrNotFound
}

func handleCommand(ctx context.Context, runtime RuntimeService, info agentsession.Info, text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(fields[0])
	if !strings.HasPrefix(cmd, "/") {
		return "", false
	}
	switch cmd {
	case "/compact":
		summary, err := runtime.CompactAuthorizedSession(ctx, info)
		if err != nil {
			return fmt.Sprintf("Compaction failed: %v", err), true
		}
		return summary, true
	default:
		return "", false
	}
}
