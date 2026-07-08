package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/credential"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	webhookplugin "github.com/CherryHQ/stella/plugins/channels/webhook"
)

const (
	// maxWebhookBody caps the request body (and therefore the agent prompt) an
	// inbound webhook may carry.
	maxWebhookBody = 256 << 10 // 256 KiB
	// webhookRequiredScope is the PAT scope required to trigger an agent. It
	// mirrors the scope the equivalent /api session-message-send route enforces
	// (agent writes). Kept literal on purpose: the ingress is auth-exempt and
	// bypasses credential.Enforce, so scope must be checked here.
	webhookRequiredScope = "agent:write"
)

// handleWebhookIngress serves POST /webhooks/{id}: an inbound-only trigger that
// runs the channel's bound agent as the PAT-authenticated caller. It is
// registered auth-exempt (registerStaticRoutes / isAuthExempt) and does its own
// PAT resolution, scope check, user-binding and agent-access authorization.
func (s *Server) handleWebhookIngress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	// --- Authentication: PAT only, honoring the resolver's 3-way contract. ---
	principal, err := s.credResolver.Resolve(ctx, r.Header.Get("Authorization"))
	if err != nil || principal == nil {
		writeError(w, http.StatusUnauthorized, "missing or invalid personal access token")
		return
	}
	if principal.Kind != credential.KindPAT {
		writeError(w, http.StatusUnauthorized, "webhook requires a personal access token")
		return
	}
	if !credential.MatchScope(principal.Scopes, webhookRequiredScope) {
		writeError(w, http.StatusForbidden, "personal access token missing the agent:write scope")
		return
	}

	// --- Load and validate the webhook instance. ---
	ch, err := s.store.GetChannel(ctx, id)
	if err != nil || ch.Type != pkgchannel.PlatformWebhook {
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	if !ch.Enabled {
		writeError(w, http.StatusConflict, "webhook is disabled")
		return
	}
	// User binding: the caller's PAT must belong to the bound user.
	if ch.UserID == "" || ch.UserID != principal.UserID {
		writeError(w, http.StatusForbidden, "personal access token does not match the webhook's bound user")
		return
	}

	// --- Resolve + authorize the bound agent. ---
	if ch.AgentID == "" {
		writeError(w, http.StatusConflict, "webhook has no bound agent")
		return
	}
	ag, err := s.store.GetAgent(ctx, ch.AgentID)
	if err != nil {
		writeError(w, http.StatusConflict, "bound agent not found")
		return
	}
	if !ag.Enabled {
		writeError(w, http.StatusConflict, "bound agent is disabled")
		return
	}
	if !s.webhookCanExecute(ctx, principal, ag) {
		writeError(w, http.StatusForbidden, "not authorized to run the bound agent")
		return
	}

	// --- Rate limit (per webhook instance). ---
	if s.webhookLimiter != nil && !s.webhookLimiter.allow(id) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// --- Config + payload. ---
	cfgMap, err := parseChannelConfig(ch.Config)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid webhook config")
		return
	}
	cfg, err := webhookplugin.DecodeConfig(cfgMap)
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
	message := strings.TrimSpace(string(body))
	if message == "" {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}

	// --- Acquire the agent service. ---
	svc := s.poolManager.GetService(ch.AgentID)
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, "agent is not available")
		return
	}

	// --- Resolve the session (ephemeral by default; persistent when configured). ---
	var info session.Info
	if cfg.Persistent() {
		key := agent.BuildUserSessionKey(ch.AgentID, principal.UserID, "webhook:"+id)
		info, err = svc.ResolveChannelSession(ctx, key, principal.UserID, ch.AgentID, session.ChannelWebhook)
	} else {
		info, err = svc.NewSession(ctx, principal.UserID, ch.AgentID, "", session.KindChat, session.ChannelWebhook)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	// --- Run the agent on a detached, bounded context (never the request ctx). ---
	runCtx, cancel := context.WithTimeout(s.runtimeCtx, time.Duration(cfg.EffectiveMaxRunTimeout())*time.Second)
	stream := svc.Chat(runCtx, agent.ChatRequest{
		SessionID: info.ID,
		UserID:    principal.UserID,
		AgentID:   ch.AgentID,
		Kind:      session.KindChat,
		Channel:   session.ChannelWebhook,
		Message:   message,
	})
	// A single drainer owns the stream for its whole life. The result channel is
	// buffered so the drainer never blocks on send after the caller stops waiting.
	resCh := drainWebhookStream(stream)

	wait := cfg.DefaultWait
	if q := r.URL.Query().Get("wait"); q != "" {
		wait = q == "true" || q == "1"
	}
	start := time.Now()

	if !wait {
		// Fire-and-forget: keep draining in the background, then release the ctx.
		go func() {
			<-resCh
			cancel()
		}()
		s.logWebhook(id, principal.UserID, ch.AgentID, "async", info.ID, "accepted", start, nil)
		writeData(w, http.StatusAccepted, map[string]any{"session_id": info.ID})
		return
	}

	// Synchronous: wait up to wait_timeout for the reply.
	select {
	case res := <-resCh:
		cancel()
		if res.err != nil {
			s.logWebhook(id, principal.UserID, ch.AgentID, "sync", info.ID, "error", start, res.err)
			writeErrorDetails(w, http.StatusBadGateway, "agent run failed", map[string]any{"session_id": info.ID})
			return
		}
		s.logWebhook(id, principal.UserID, ch.AgentID, "sync", info.ID, "ok", start, nil)
		writeData(w, http.StatusOK, map[string]any{"session_id": info.ID, "output": res.output})
	case <-time.After(time.Duration(cfg.EffectiveWaitTimeout()) * time.Second):
		// Stop waiting, but the drainer keeps consuming the stream to completion.
		go func() {
			<-resCh
			cancel()
		}()
		s.logWebhook(id, principal.UserID, ch.AgentID, "sync", info.ID, "timeout", start, nil)
		writeErrorDetails(w, http.StatusGatewayTimeout, "timed out waiting for agent reply", map[string]any{"session_id": info.ID})
	}
}

type webhookResult struct {
	output string
	err    error
}

// drainWebhookStream consumes the whole agent event stream, accumulating text.
// The returned channel is buffered(1) so the goroutine can always deliver its
// result even if nobody is waiting (e.g. after a 504).
func drainWebhookStream(stream <-chan agent.Event) <-chan webhookResult {
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
		res <- webhookResult{output: b.String(), err: runErr}
	}()
	return res
}

// webhookCanExecute reports whether the PAT's user may run the given agent.
// Mirrors the channel-layer ResolveAgent authorization (identity.go).
func (s *Server) webhookCanExecute(ctx context.Context, principal *credential.Principal, ag config.Agent) bool {
	assignedIDs, _ := s.authStore.ListUserAgentIDs(ctx, principal.UserID)
	role := principal.Role
	if role == "" {
		role = auth.RoleUser
	}
	return s.engine.Can(ctx, auth.AccessRequest{
		Subject: auth.Subject{
			UserID:   principal.UserID,
			Roles:    []string{role},
			AgentIDs: assignedIDs,
		},
		Action: auth.ActionExecute,
		Resource: auth.Resource{
			Type:  auth.ResourceAgent,
			ID:    ag.ID,
			Attrs: map[string]any{"scope": ag.Scope},
		},
	})
}

// logWebhook emits the structured audit record for one webhook invocation.
func (s *Server) logWebhook(webhookID, userID, agentID, mode, sessionID, status string, start time.Time, err error) {
	s.log.Info("webhook ingress",
		"webhook_id", webhookID,
		"user_id", userID,
		"agent_id", agentID,
		"mode", mode,
		"session_id", sessionID,
		"status", status,
		"duration_ms", time.Since(start).Milliseconds(),
		"error", err,
	)
}
