package server

import (
	"context"
	"net/http"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/webhook"
)

// webhookRunPort is the ingress-only view of agent execution. It keeps concrete
// PoolManager return types at composition while letting the capability boundary
// prove its fixed identity and admission handling without a live model runtime.
// webhookIngressPort is the narrow capability-domain surface this transport
// needs. The production adapter is the one shared webhook.Service; tests can
// prove transport behavior without recreating its encrypted persistence domain.
type webhookIngressPort interface {
	ResolveCapability(context.Context, string) (webhook.Invocation, error)
	ValidateGitHub(webhook.Invocation, http.Header, []byte, webhook.GitHubPolicy) (webhook.GitHubDelivery, error)
	ClaimGitHubDelivery(context.Context, webhook.GitHubDelivery) (bool, error)
	ReleaseGitHubDelivery(context.Context, webhook.GitHubDelivery) (bool, error)
}

type webhookServiceIngressPort struct{ svc *webhook.Service }

func (p webhookServiceIngressPort) ResolveCapability(ctx context.Context, raw string) (webhook.Invocation, error) {
	return p.svc.ResolveCapability(ctx, raw)
}

func (p webhookServiceIngressPort) ValidateGitHub(inv webhook.Invocation, header http.Header, body []byte, policy webhook.GitHubPolicy) (webhook.GitHubDelivery, error) {
	return p.svc.ValidateGitHub(inv, header, body, policy)
}

func (p webhookServiceIngressPort) ClaimGitHubDelivery(ctx context.Context, delivery webhook.GitHubDelivery) (bool, error) {
	return p.svc.ClaimGitHubDelivery(ctx, delivery)
}

func (p webhookServiceIngressPort) ReleaseGitHubDelivery(ctx context.Context, delivery webhook.GitHubDelivery) (bool, error) {
	return p.svc.ReleaseGitHubDelivery(ctx, delivery)
}

type webhookRunPort interface {
	Get(string) webhookAgentRun
}

type webhookAgentRun interface {
	ResolvePrivateChannelSession(context.Context, authz.Authority, string, string, string, session.Channel) (session.Info, error)
	NewSession(context.Context, authz.Authority, string, string, string, session.Kind, session.Channel) (session.Info, error)
	ChatAdmitted(context.Context, agent.ChatRequest) (<-chan agent.Event, error)
}

type poolWebhookRunPort struct {
	pool interface{ GetService(string) *agent.Service }
}

func (p poolWebhookRunPort) Get(agentID string) webhookAgentRun {
	if p.pool == nil {
		return nil
	}
	return p.pool.GetService(agentID)
}
