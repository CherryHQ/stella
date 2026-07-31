package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/agenterr"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/webhook"
)

const (
	// maxWebhookBody caps the request body (and therefore the agent prompt) an
	// inbound webhook may carry. The dedicated ingress slot, read deadline, and
	// MaxBytesReader refinement are reserved for a later phase.
	maxWebhookBody = 256 << 10 // 256 KiB
	// webhookSessionCleanupTimeout bounds the cancellation-detached archive of an
	// ephemeral session orphaned by a post-create admission failure. It is a small
	// fixed ceiling; raise only if session archival legitimately needs longer.
	webhookSessionCleanupTimeout = 5 * time.Second
)

// errWebhookAgentUnavailable and errWebhookTooManyRuns are internal admission
// outcomes the callback returns to the transport for response mapping.
var (
	errWebhookAgentUnavailable = errors.New("webhook: agent unavailable")
	errWebhookTooManyRuns      = errors.New("webhook: too many concurrent runs")
)

// handleWebhookIngress serves the sanitized capability request the reservation
// dispatched inward. The opaque URL capability (carried in private context) is
// the sole credential: the webhook module resolves and revalidates the fixed
// owner→Agent identity, so the caller cannot choose a user or Agent and any
// Authorization header is ignored.
//
// It runs the two-stage admission: ResolveCandidate before the body read, then
// Admit — which revalidates the current credential, the durable active state,
// and the owner's Agent-use permission under the lifecycle read lock — invoking
// the callback that synchronously admits the turn. The response is written only
// after admission (not completion).
func (s *Server) handleWebhookIngress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.webhookIngress == nil {
		writeError(w, http.StatusServiceUnavailable, "webhook ingress unavailable")
		return
	}
	capability, ok := webhookCapabilityFromContext(ctx)
	if !ok {
		// Reached without the reservation's parsing: refuse opaquely.
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	}

	cand, err := s.webhookIngress.ResolveCandidate(ctx, capability)
	if err != nil {
		if errors.Is(err, webhook.ErrNotFound) {
			writeError(w, http.StatusNotFound, "webhook not found")
			return
		}
		s.log.ErrorContext(ctx, "resolve webhook candidate", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve webhook")
		return
	}

	// Acceptance limiter keyed on the safe endpoint id, before the body read.
	if s.webhookLimiter != nil && !s.webhookLimiter.allow(cand.EndpointID) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// Channel run behavior only (session/reply/timeouts); identity is on the
	// endpoint and revalidated inside Admit.
	ch, err := s.channelResolver.Channel(ctx, cand.EndpointID)
	if err != nil {
		s.log.ErrorContext(ctx, "load webhook channel", "endpoint_id", cand.EndpointID, "error", err)
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
	message := strings.TrimSpace(string(body))
	if message == "" {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}

	// Reply mode: the reservation already validated and typed any wait override.
	// Compute it once so every audit record (including a busy rejection) reports
	// the mode the caller actually requested.
	wait := cfg.DefaultWait
	if override, ok := webhookWaitFromContext(ctx); ok {
		wait = override
	}
	mode := "async"
	if wait {
		mode = "sync"
	}

	var (
		stream           <-chan agent.Event
		info             session.Info
		invoked          webhook.AdmittedInvocation
		done             func()
		start            time.Time
		createdEphemeral bool
	)
	admitErr := s.webhookIngress.Admit(ctx, cand, func(admitCtx context.Context, inv webhook.AdmittedInvocation) error {
		invoked = inv
		run := s.webhookRun.Get(inv.AgentID)
		if run == nil {
			return errWebhookAgentUnavailable
		}
		// Reserve the in-flight run slot before any session work, so an
		// at-capacity rejection creates/resolves no session. Every pre-admission
		// failure below releases it exactly once; on success `done` takes
		// ownership of the single release.
		if s.webhookLimiter != nil && !s.webhookLimiter.beginRun(inv.ChannelID) {
			return errWebhookTooManyRuns
		}
		releaseSlot := func() {
			if s.webhookLimiter != nil {
				s.webhookLimiter.endRun(inv.ChannelID)
			}
		}
		var serr error
		if cfg.Persistent {
			// A persistent session pre-exists this request; it must never be
			// archived on failure.
			key := agent.BuildUserSessionKey(inv.AgentID, inv.OwnerUserID, "webhook:"+inv.ChannelID)
			info, serr = run.ResolvePrivateChannelSession(admitCtx, inv.Authority, key, inv.OwnerUserID, inv.AgentID, session.ChannelWebhook)
		} else {
			info, serr = run.NewSession(admitCtx, inv.Authority, inv.OwnerUserID, inv.AgentID, "", session.KindChat, session.ChannelWebhook)
			if serr == nil {
				// This callback created a fresh ephemeral session; a later failure
				// must compensate it.
				createdEphemeral = true
			}
		}
		if serr != nil {
			releaseSlot()
			return errors.Join(errWebhookAgentUnavailable, serr)
		}
		// Detached, bounded run context (never the request ctx): the run may
		// outlive this request and must survive graceful-drain cancellation.
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(s.runtimeCtx), time.Duration(cfg.MaxRunTimeoutSeconds)*time.Second)
		st, aerr := run.ChatAdmitted(runCtx, agent.ChatRequest{
			SessionID: info.ID,
			UserID:    inv.OwnerUserID,
			AgentID:   inv.AgentID,
			Kind:      session.KindChat,
			Channel:   session.ChannelWebhook,
			Message:   message,
			Authority: inv.Authority,
		})
		if aerr != nil {
			cancel()
			releaseSlot()
			return aerr
		}
		stream = st
		start = time.Now()
		done = func() {
			cancel()
			releaseSlot()
		}
		return nil
	})

	// Pre-admission compensation: if this callback created a fresh ephemeral
	// session but admission then failed (any post-create error, including a busy
	// runtime), archive that exact session so no active empty session is left
	// behind. The run slot was already released exactly once in the callback.
	if admitErr != nil && createdEphemeral {
		s.archiveOrphanedWebhookSession(invoked, info.ID)
	}

	switch {
	case admitErr == nil:
		// Admitted; fall through to the response.
	case errors.Is(admitErr, webhook.ErrNotFound), errors.Is(admitErr, webhook.ErrOwnerAgentForbidden):
		// Rotated/revoked/inactive during body read, or the owner's permission
		// was withdrawn: fail closed and opaque.
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	case errors.Is(admitErr, agenterr.ErrSessionBusy):
		s.logWebhook(cand.EndpointID, invoked.OwnerUserID, invoked.AgentID, mode, info.ID, "busy", time.Now(), admitErr)
		writeWebhookBusy(w, info.ID)
		return
	case errors.Is(admitErr, errWebhookTooManyRuns):
		writeError(w, http.StatusTooManyRequests, "too many concurrent runs for this webhook")
		return
	case errors.Is(admitErr, errWebhookAgentUnavailable):
		writeError(w, http.StatusServiceUnavailable, "agent is not available")
		return
	default:
		s.log.ErrorContext(ctx, "admit webhook", "endpoint_id", cand.EndpointID, "error", admitErr)
		writeError(w, http.StatusInternalServerError, "failed to admit webhook")
		return
	}

	// A single drainer owns the admitted stream for its whole life.
	resCh := drainWebhookStream(stream)

	if !wait {
		s.logWebhook(cand.EndpointID, invoked.OwnerUserID, invoked.AgentID, mode, info.ID, "accepted", start, nil)
		s.finishWebhookRun(cand.EndpointID, invoked.OwnerUserID, invoked.AgentID, mode, info.ID, start, resCh, done)
		writeData(w, http.StatusAccepted, map[string]any{"session_id": info.ID})
		return
	}

	select {
	case res := <-resCh:
		done()
		if res.err != nil {
			s.logWebhook(cand.EndpointID, invoked.OwnerUserID, invoked.AgentID, mode, info.ID, "error", start, res.err)
			writeErrorDetails(w, http.StatusBadGateway, "agent run failed", map[string]any{"session_id": info.ID})
			return
		}
		s.logWebhook(cand.EndpointID, invoked.OwnerUserID, invoked.AgentID, mode, info.ID, "ok", start, nil)
		writeData(w, http.StatusOK, map[string]any{"session_id": info.ID, "output": res.output})
	case <-time.After(time.Duration(cfg.WaitTimeoutSeconds) * time.Second):
		s.logWebhook(cand.EndpointID, invoked.OwnerUserID, invoked.AgentID, mode, info.ID, "timeout", start, nil)
		s.finishWebhookRun(cand.EndpointID, invoked.OwnerUserID, invoked.AgentID, mode, info.ID, start, resCh, done)
		writeErrorDetails(w, http.StatusGatewayTimeout, "timed out waiting for agent reply", map[string]any{"session_id": info.ID})
	}
}

// archiveOrphanedWebhookSession archives an ephemeral session created by a failed
// admission, using the same fixed authority/owner/agent and a cancellation-
// detached, bounded context (the request context may already be done). It is
// best-effort: a cleanup failure is logged without any secret and never changes
// the original admission outcome.
func (s *Server) archiveOrphanedWebhookSession(inv webhook.AdmittedInvocation, sessionID string) {
	if s.webhookRun == nil || sessionID == "" {
		return
	}
	run := s.webhookRun.Get(inv.AgentID)
	if run == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.runtimeCtx), webhookSessionCleanupTimeout)
	defer cancel()
	if err := run.ArchiveSession(ctx, inv.Authority, inv.OwnerUserID, inv.AgentID, sessionID); err != nil {
		s.log.Error("archive orphaned webhook session", "endpoint_id", inv.ChannelID, "session_id", sessionID, "error", err)
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
