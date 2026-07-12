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
	"github.com/CherryHQ/stella/internal/credential"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	webhookplugin "github.com/CherryHQ/stella/plugins/channels/webhook"
)

const (
	// maxWebhookBody caps the request body (and therefore the agent prompt) an
	// inbound webhook may carry.
	maxWebhookBody = 256 << 10 // 256 KiB
	// webhookBusyPeekWindow is how long the fire-and-forget path watches the
	// stream before acknowledging. ErrSessionBusy is emitted synchronously
	// before any work starts, so a busy rejection lands inside this window; a
	// real run does not. Without the peek, a 202 would acknowledge a message
	// the runtime already dropped.
	webhookBusyPeekWindow = 100 * time.Millisecond
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
	// The ingress is auth-exempt and bypasses credential.Enforce, so the scope
	// must be checked here; the constant is pinned to the /api route mapping in
	// the credential package.
	if !credential.MatchScope(principal.Scopes, credential.ScopeAgentWrite) {
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

	// --- Resolve + authorize the bound agent. ---
	// There is no static user binding: the run executes as the caller (the PAT's
	// user), gated by whether that user may execute the bound agent below.
	if ch.AgentID == "" {
		writeError(w, http.StatusConflict, "webhook has no bound agent")
		return
	}
	authority, err := principal.Authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "not authorized to run the bound agent")
		return
	}
	ag, err := s.agentAccess.Use(ctx, authority, ch.AgentID)
	if err != nil {
		code, msg := agentAccessError(err)
		if code == http.StatusNotFound {
			code, msg = http.StatusConflict, "bound agent not found"
		}
		writeError(w, code, msg)
		return
	}
	if !ag.Enabled {
		writeError(w, http.StatusConflict, "bound agent is disabled")
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
	// Parse the reply-mode override before anything irreversible: a malformed
	// value must reject the request, not silently pick a mode (or worse,
	// reject after the run already started).
	wait := cfg.DefaultWait
	if q := r.URL.Query().Get("wait"); q != "" {
		v, perr := strconv.ParseBool(q)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "invalid wait parameter: use true or false")
			return
		}
		wait = v
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
		info, err = svc.ResolvePrivateChannelSession(ctx, key, principal.UserID, ch.AgentID, session.ChannelWebhook)
	} else {
		info, err = svc.NewSession(ctx, principal.UserID, ch.AgentID, "", session.KindChat, session.ChannelWebhook)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	// --- Cap concurrent runs: a run can outlive this request by minutes, so
	// the acceptance-rate bucket alone cannot bound resource usage. ---
	if s.webhookLimiter != nil && !s.webhookLimiter.beginRun(id) {
		writeError(w, http.StatusTooManyRequests, "too many concurrent runs for this webhook")
		return
	}

	// --- Run the agent on a detached, bounded context (never the request ctx). ---
	runCtx, cancel := context.WithTimeout(s.runtimeCtx, time.Duration(cfg.EffectiveMaxRunTimeout())*time.Second)
	// done releases everything tied to the run's lifetime; every path that
	// consumes the drain result calls it exactly once.
	done := func() {
		cancel()
		if s.webhookLimiter != nil {
			s.webhookLimiter.endRun(id)
		}
	}
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

	start := time.Now()

	if !wait {
		// Fire-and-forget — but peek briefly so an immediate busy rejection
		// (persistent session with a turn already running) becomes a 429
		// instead of a 202 for a message that was never processed.
		if res, ok := peekWebhookResult(resCh, webhookBusyPeekWindow); ok {
			done()
			if errors.Is(res.err, agenterr.ErrSessionBusy) {
				s.logWebhook(id, principal.UserID, ch.AgentID, "async", info.ID, "busy", start, res.err)
				writeWebhookBusy(w, info.ID)
				return
			}
			// The run finished inside the peek window; record its terminal
			// status now, the response stays 202 for a stable contract.
			s.logWebhook(id, principal.UserID, ch.AgentID, "async", info.ID, webhookRunStatus(res.err), start, res.err)
		} else {
			s.logWebhook(id, principal.UserID, ch.AgentID, "async", info.ID, "accepted", start, nil)
			s.finishWebhookRun(id, principal.UserID, ch.AgentID, "async", info.ID, start, resCh, done)
		}
		writeData(w, http.StatusAccepted, map[string]any{"session_id": info.ID})
		return
	}

	// Synchronous: wait up to wait_timeout for the reply.
	select {
	case res := <-resCh:
		done()
		if res.err != nil {
			if errors.Is(res.err, agenterr.ErrSessionBusy) {
				s.logWebhook(id, principal.UserID, ch.AgentID, "sync", info.ID, "busy", start, res.err)
				writeWebhookBusy(w, info.ID)
				return
			}
			s.logWebhook(id, principal.UserID, ch.AgentID, "sync", info.ID, "error", start, res.err)
			writeErrorDetails(w, http.StatusBadGateway, "agent run failed", map[string]any{"session_id": info.ID})
			return
		}
		s.logWebhook(id, principal.UserID, ch.AgentID, "sync", info.ID, "ok", start, nil)
		writeData(w, http.StatusOK, map[string]any{"session_id": info.ID, "output": res.output})
	case <-time.After(time.Duration(cfg.EffectiveWaitTimeout()) * time.Second):
		// Stop waiting, but the drainer keeps consuming the stream to
		// completion; the terminal status lands in the log.
		s.logWebhook(id, principal.UserID, ch.AgentID, "sync", info.ID, "timeout", start, nil)
		s.finishWebhookRun(id, principal.UserID, ch.AgentID, "sync", info.ID, start, resCh, done)
		writeErrorDetails(w, http.StatusGatewayTimeout, "timed out waiting for agent reply", map[string]any{"session_id": info.ID})
	}
}

// finishWebhookRun consumes the run result in the background after the HTTP
// response has been written, releases the run's resources via done, and emits
// the terminal audit record — the log line is the only place the final
// outcome is visible.
func (s *Server) finishWebhookRun(webhookID, userID, agentID, mode, sessionID string, start time.Time, resCh <-chan webhookResult, done func()) {
	go func() {
		res := <-resCh
		done()
		s.logWebhook(webhookID, userID, agentID, mode, sessionID, webhookRunStatus(res.err), start, res.err)
	}()
}

// peekWebhookResult waits up to window for a result that is already (or about
// to be) available, reporting whether one arrived.
func peekWebhookResult(resCh <-chan webhookResult, window time.Duration) (webhookResult, bool) {
	select {
	case res := <-resCh:
		return res, true
	case <-time.After(window):
		return webhookResult{}, false
	}
}

// webhookRunStatus maps a terminal run error to the audit-log status value.
func webhookRunStatus(err error) string {
	if err != nil {
		return "failed"
	}
	return "done"
}

// writeWebhookBusy reports a busy persistent session: the message was NOT
// processed and the caller should retry once the in-flight turn finishes.
func writeWebhookBusy(w http.ResponseWriter, sessionID string) {
	w.Header().Set("Retry-After", "1")
	writeErrorDetails(w, http.StatusTooManyRequests, "session is busy with another run; retry later", map[string]any{"session_id": sessionID})
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
