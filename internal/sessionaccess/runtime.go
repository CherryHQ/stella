package sessionaccess

import (
	"context"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
)

// RuntimeManager is the typed runtime lookup port used by session Send/Attach.
// Tests can provide a deterministic in-memory implementation; production wraps
// the agent pool with NewRuntimeManager.
type RuntimeManager interface {
	GetService(agentID string) RuntimeService
	Default() RuntimeService
}

// AgentServiceManager is the production pool shape. The adapter keeps the
// sessionaccess port narrow despite agent.PoolManager returning concrete
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
// port without making internal/agent import sessionaccess.
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
	return m.inner.GetService(agentID)
}

func (m agentRuntimeManager) Default() RuntimeService {
	if m.inner == nil {
		return nil
	}
	return m.inner.Default()
}

// RuntimeService is the narrow live-turn port sessionaccess needs from the
// agent runtime. It deliberately excludes session lookup and policy concerns.
type RuntimeService interface {
	Chat(context.Context, agent.ChatRequest) <-chan agent.Event
	SubscribeSession(sessionID string) (<-chan agent.Event, func())
	SessionLive(sessionID string) bool
	CompactSession(context.Context, agentsession.Info) (string, error)
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
// chunks are not re-authorized by sessionaccess; the single Access evaluation
// covers the turn initiation, matching the send semantics expected by the UI.
func (s *Service) Send(ctx context.Context, in SendInput) (SendResult, error) {
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
	ch := runtime.Chat(ctx, agent.ChatRequest{
		SessionID: in.SessionID,
		UserID:    info.UserID,
		AgentID:   info.AgentID,
		Kind:      agentsession.Kind(info.Kind),
		Channel:   agentsession.Channel(info.Channel),
		Message:   in.Message,
		Authority: in.Authority,
	})
	return SendResult{Events: ch}, nil
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
	BeforeProtectedEvent func(context.Context) error
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
	return AttachResult{
		Events: ch,
		Cancel: cancel,
		Live:   runtime.SessionLive(in.SessionID),
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
		summary, err := runtime.CompactSession(ctx, info)
		if err != nil {
			return fmt.Sprintf("Compaction failed: %v", err), true
		}
		return summary, true
	default:
		return "", false
	}
}
