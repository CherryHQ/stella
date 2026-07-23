package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/agenterr"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/webhook"
)

const (
	// maxWebhookBody caps the request body (and therefore the agent prompt) an
	// inbound webhook may carry.
	maxWebhookBody              = 256 << 10 // 256 KiB
	webhookDeliveryStoreTimeout = 5 * time.Second
)

// handleWebhookIngress serves POST /webhooks/{capability}. It is auth-exempt
// because the opaque URL capability is the credential: webhook.Service resolves
// the endpoint's fixed owner, Agent, and WorkerAgentAuthority before this
// transport can create a session. Authorization headers are deliberately ignored
// and can never alter that durable identity.
func (s *Server) handleWebhookIngress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	capability := r.PathValue("capability")
	invocation, err := s.webhookIngress.ResolveCapability(ctx, capability)
	if err != nil {
		if errors.Is(err, webhook.ErrNotFound) {
			writeError(w, http.StatusNotFound, "webhook not found")
			return
		}
		s.log.ErrorContext(ctx, "resolve webhook capability", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve webhook")
		return
	}

	ch, err := s.channelResolver.Channel(ctx, invocation.Endpoint.ChannelID)
	if err != nil {
		s.log.ErrorContext(ctx, "load webhook channel configuration", "endpoint_id", invocation.Endpoint.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "invalid webhook config")
		return
	}
	cfgMap, err := parseChannelConfig(ch.Config)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid webhook config")
		return
	}
	cfg, err := s.pluginHost.DecodeWebhookRunConfig(cfgMap)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid webhook config")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if len(body) > maxWebhookBody {
		writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}

	message := ""
	var githubDelivery webhook.GitHubDelivery
	githubClaimed := false
	if invocation.Endpoint.Provider == webhook.ProviderGitHub {
		// GitHub is intentionally fire-and-forget. A synchronous reply would put
		// Agent output in GitHub's delivery UI; callers asking for one get an
		// explicit error rather than a silent semantic change.
		if q := r.URL.Query().Get("wait"); q != "" {
			wait, parseErr := strconv.ParseBool(q)
			if parseErr != nil {
				writeError(w, http.StatusBadRequest, "invalid wait parameter: use true or false")
				return
			}
			if wait {
				writeError(w, http.StatusBadRequest, "GitHub webhooks do not support wait=true")
				return
			}
		}
		endpointCfg, err := s.pluginHost.DecodeWebhookEndpointConfig(cfgMap)
		if err != nil || endpointCfg.Provider != string(webhook.ProviderGitHub) {
			writeError(w, http.StatusInternalServerError, "invalid webhook config")
			return
		}
		githubDelivery, err = s.webhookIngress.ValidateGitHub(invocation, r.Header, body, webhook.GitHubPolicy{
			Events: endpointCfg.GitHubEvents, Repositories: endpointCfg.GitHubRepositories,
		})
		switch {
		case errors.Is(err, webhook.ErrGitHubDeliveryIgnored):
			writeGitHubAccepted(w)
			return
		case errors.Is(err, webhook.ErrInvalidGitHubDelivery):
			writeError(w, http.StatusUnauthorized, "invalid GitHub webhook delivery")
			return
		case err != nil:
			s.log.ErrorContext(ctx, "validate GitHub webhook delivery", "endpoint_id", invocation.Endpoint.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to validate GitHub webhook delivery")
			return
		}
	} else {
		message = strings.TrimSpace(string(body))
		if message == "" {
			writeError(w, http.StatusBadRequest, "empty request body")
			return
		}
	}

	wait := cfg.DefaultWait
	if invocation.Endpoint.Provider == webhook.ProviderGitHub {
		wait = false
	} else if q := r.URL.Query().Get("wait"); q != "" {
		wait, err = strconv.ParseBool(q)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid wait parameter: use true or false")
			return
		}
	}

	// Only valid, policy-accepted deliveries consume a per-endpoint acceptance
	// token. GitHub validation deliberately precedes both this limiter and claim.
	// Claim stays after the limiter because GitHub does not sign the delivery ID:
	// a captured signed body with varied IDs must not create unbounded DB rows.
	// Consequently, duplicate retries consume a token by design.
	if s.webhookLimiter != nil && !s.webhookLimiter.allow(invocation.Endpoint.ID) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	if invocation.Endpoint.Provider == webhook.ProviderGitHub {
		claimed, err := s.claimGitHubDelivery(ctx, githubDelivery)
		if err != nil {
			s.log.ErrorContext(ctx, "claim GitHub webhook delivery", "endpoint_id", invocation.Endpoint.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to claim GitHub webhook delivery")
			return
		}
		if !claimed {
			writeGitHubAccepted(w)
			return
		}
		githubClaimed = true
		envelope, err := githubDelivery.Envelope()
		if err != nil {
			s.releaseGitHubDelivery(ctx, invocation.Endpoint.ID, githubDelivery)
			writeError(w, http.StatusInternalServerError, "failed to prepare GitHub webhook delivery")
			return
		}
		message = string(envelope)
	}

	svc := s.webhookRun.Get(invocation.AgentID)
	if svc == nil {
		if githubClaimed {
			s.releaseGitHubDelivery(ctx, invocation.Endpoint.ID, githubDelivery)
		}
		writeError(w, http.StatusServiceUnavailable, "agent is not available")
		return
	}

	if s.webhookLimiter != nil && !s.webhookLimiter.beginRun(invocation.Endpoint.ID) {
		if githubClaimed {
			s.releaseGitHubDelivery(ctx, invocation.Endpoint.ID, githubDelivery)
		}
		writeError(w, http.StatusTooManyRequests, "too many concurrent runs for this webhook")
		return
	}

	var info session.Info
	if cfg.Persistent {
		key := agent.BuildUserSessionKey(invocation.AgentID, invocation.Endpoint.OwnerUserID, "webhook:"+invocation.Endpoint.ID)
		info, err = svc.ResolvePrivateChannelSession(ctx, invocation.Authority, key, invocation.Endpoint.OwnerUserID, invocation.AgentID, session.ChannelWebhook)
	} else {
		info, err = svc.NewSession(ctx, invocation.Authority, invocation.Endpoint.OwnerUserID, invocation.AgentID, "", session.KindChat, session.ChannelWebhook)
	}
	if err != nil {
		if s.webhookLimiter != nil {
			s.webhookLimiter.endRun(invocation.Endpoint.ID)
		}
		if githubClaimed {
			s.releaseGitHubDelivery(ctx, invocation.Endpoint.ID, githubDelivery)
		}
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	runCtx, cancel := context.WithTimeout(context.WithoutCancel(s.runtimeCtx), time.Duration(cfg.MaxRunTimeoutSeconds)*time.Second)
	done := func() {
		cancel()
		if s.webhookLimiter != nil {
			s.webhookLimiter.endRun(invocation.Endpoint.ID)
		}
	}
	stream, err := svc.ChatAdmitted(runCtx, agent.ChatRequest{
		SessionID: info.ID, UserID: invocation.Endpoint.OwnerUserID, AgentID: invocation.AgentID,
		Kind: session.KindChat, Channel: session.ChannelWebhook, Message: message, Authority: invocation.Authority,
	})
	if err != nil {
		done()
		if githubClaimed {
			s.releaseGitHubDelivery(ctx, invocation.Endpoint.ID, githubDelivery)
		}
		if errors.Is(err, agenterr.ErrSessionBusy) {
			if githubClaimed {
				writeError(w, http.StatusServiceUnavailable, "GitHub webhook session is busy; retry later")
				return
			}
			writeWebhookBusy(w, info.ID)
			return
		}
		writeError(w, http.StatusServiceUnavailable, "agent did not admit webhook run; retry later")
		return
	}
	resCh := drainWebhookStream(runCtx, stream)
	start := time.Now()

	if !wait {
		// ChatAdmitted is the admission boundary: once it returned a stream, the
		// delivery claim is retained even when the runtime fails immediately.
		s.logWebhook(invocation.Endpoint.ID, invocation.Endpoint.OwnerUserID, invocation.AgentID, "async", info.ID, "accepted", start, nil)
		s.finishWebhookRun(invocation.Endpoint.ID, invocation.Endpoint.OwnerUserID, invocation.AgentID, "async", info.ID, start, resCh, done)
		if githubClaimed {
			writeGitHubAccepted(w)
			return
		}
		writeData(w, http.StatusAccepted, map[string]any{"session_id": info.ID})
		return
	}

	select {
	case res := <-resCh:
		done()
		if res.err != nil {
			if errors.Is(res.err, agenterr.ErrSessionBusy) {
				s.logWebhook(invocation.Endpoint.ID, invocation.Endpoint.OwnerUserID, invocation.AgentID, "sync", info.ID, "busy", start, res.err)
				writeWebhookBusy(w, info.ID)
				return
			}
			s.logWebhook(invocation.Endpoint.ID, invocation.Endpoint.OwnerUserID, invocation.AgentID, "sync", info.ID, "error", start, res.err)
			writeErrorDetails(w, http.StatusBadGateway, "agent run failed", map[string]any{"session_id": info.ID})
			return
		}
		s.logWebhook(invocation.Endpoint.ID, invocation.Endpoint.OwnerUserID, invocation.AgentID, "sync", info.ID, "ok", start, nil)
		writeData(w, http.StatusOK, map[string]any{"session_id": info.ID, "output": res.output})
	case <-time.After(time.Duration(cfg.WaitTimeoutSeconds) * time.Second):
		s.logWebhook(invocation.Endpoint.ID, invocation.Endpoint.OwnerUserID, invocation.AgentID, "sync", info.ID, "timeout", start, nil)
		s.finishWebhookRun(invocation.Endpoint.ID, invocation.Endpoint.OwnerUserID, invocation.AgentID, "sync", info.ID, start, resCh, done)
		writeErrorDetails(w, http.StatusGatewayTimeout, "timed out waiting for agent reply", map[string]any{"session_id": info.ID})
	}
}

func (s *Server) claimGitHubDelivery(ctx context.Context, delivery webhook.GitHubDelivery) (bool, error) {
	// Claiming must outlive the caller for the same reason as release: an HTTP
	// client cancellation during COMMIT must not leave the outcome ambiguous.
	claimCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), webhookDeliveryStoreTimeout)
	defer cancel()
	return s.webhookIngress.ClaimGitHubDelivery(claimCtx, delivery)
}

func (s *Server) releaseGitHubDelivery(ctx context.Context, endpointID string, delivery webhook.GitHubDelivery) {
	// Releasing a pre-admission claim must outlive the caller: GitHub can cancel
	// its request before Stella returns the retryable response. Keep request
	// values for tracing, but detach cancellation and bound the cleanup itself.
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), webhookDeliveryStoreTimeout)
	defer cancel()
	if _, err := s.webhookIngress.ReleaseGitHubDelivery(releaseCtx, delivery); err != nil {
		s.log.ErrorContext(releaseCtx, "release GitHub webhook delivery", "endpoint_id", endpointID, "error", err)
	}
}

// finishWebhookRun consumes the run result in the background after the HTTP
// response has been written, releases the run's resources via done, and emits
// the terminal audit record.
func (s *Server) finishWebhookRun(webhookID, userID, agentID, mode, sessionID string, start time.Time, resCh <-chan webhookResult, done func()) {
	go func() {
		res := <-resCh
		done()
		s.logWebhook(webhookID, userID, agentID, mode, sessionID, webhookRunStatus(res.err), start, res.err)
	}()
}

func webhookRunStatus(err error) string {
	if err != nil {
		return "failed"
	}
	return "done"
}

// writeGitHubAccepted deliberately returns no Agent output: GitHub deliveries
// are always asynchronous and their external contract is a bare 202.
func writeGitHubAccepted(w http.ResponseWriter) {
	w.WriteHeader(http.StatusAccepted)
}

func writeWebhookBusy(w http.ResponseWriter, sessionID string) {
	w.Header().Set("Retry-After", "1")
	writeErrorDetails(w, http.StatusTooManyRequests, "session is busy with another run; retry later", map[string]any{"session_id": sessionID})
}

type webhookResult struct {
	output string
	err    error
}

func drainWebhookStream(ctx context.Context, stream <-chan agent.Event) <-chan webhookResult {
	res := make(chan webhookResult, 1)
	go func() {
		var b strings.Builder
		var runErr error
		for ev := range stream {
			if ev.Text != "" {
				b.WriteString(ev.Text)
			}
			if ev.Err != nil {
				runErr = ev.Err
			}
		}
		// The runtime deliberately drops terminal events after its context is
		// canceled. Preserve that cancellation here so timeout and shutdown runs
		// cannot be audited as successful merely because the stream closed cleanly.
		if runErr == nil {
			runErr = ctx.Err()
		}
		res <- webhookResult{output: b.String(), err: runErr}
	}()
	return res
}

// logWebhook emits the structured audit record for one endpoint invocation.
// webhookID is always the durable endpoint ID, never the URL capability.
func (s *Server) logWebhook(webhookID, userID, agentID, mode, sessionID, status string, start time.Time, err error) {
	s.log.Info("webhook ingress",
		"endpoint_id", webhookID,
		"user_id", userID,
		"agent_id", agentID,
		"mode", mode,
		"session_id", sessionID,
		"status", status,
		"duration_ms", time.Since(start).Milliseconds(),
		"error", err,
	)
}
