package server

import (
	"context"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/webhook"
)

// webhookIngressPort is the deep capability-admission surface the transport
// consumes. The production implementation is the shared *webhook.Service; tests
// supply a fake to drive candidate/admit ordering without its persistence.
type webhookIngressPort interface {
	ResolveCandidate(context.Context, string) (webhook.Candidate, error)
	Admit(context.Context, webhook.Candidate, webhook.AdmitCallback) error
}

// webhookRunPort is the narrow agent-execution surface the ingress callback uses.
// It keeps the concrete PoolManager out of the transport so a test can prove the
// admission/response behavior (exactly one ChatAdmitted, fixed authority, wait
// handling) without a live model runtime.
type webhookRunPort interface {
	Get(agentID string) webhookAgentRun
}

type webhookAgentRun interface {
	ResolvePrivateChannelSession(context.Context, authz.Authority, string, string, string, session.Channel) (session.Info, error)
	NewSession(context.Context, authz.Authority, string, string, string, session.Kind, session.Channel) (session.Info, error)
	ChatAdmitted(context.Context, agent.ChatRequest) (<-chan agent.Event, error)
	// ArchiveSession compensates a freshly created ephemeral session when
	// admission fails after the session was persisted but before a turn ran.
	ArchiveSession(context.Context, authz.Authority, string, string, string) error
}

type poolWebhookRunPort struct {
	pool interface{ GetService(string) *agent.Service }
}

func (p poolWebhookRunPort) Get(agentID string) webhookAgentRun {
	if p.pool == nil {
		return nil
	}
	svc := p.pool.GetService(agentID)
	if svc == nil {
		return nil
	}
	return svc
}
