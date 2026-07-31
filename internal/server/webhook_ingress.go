package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/agenterr"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/webhook"
)

const (
	// maxWebhookBody caps the request body (and therefore the agent prompt) an
	// inbound webhook may carry. It is enforced with http.MaxBytesReader under the
	// per-endpoint ingress slot and the body-read deadline below.
	maxWebhookBody = 256 << 10 // 256 KiB
	// webhookBodyReadTimeout bounds how long the bounded body read may occupy an
	// ingress slot waiting on a slow client. It is a fixed ceiling; raise only if
	// legitimate clients streaming a near-256 KiB body over slow links are seen
	// timing out. The real server enforces it via the connection read deadline.
	webhookBodyReadTimeout = 30 * time.Second
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

	// Ingress resource gate: a bounded pre-admission slot per endpoint, acquired
	// after candidate resolution and before any body read. It covers only the
	// bounded body read and synchronous admission — NOT the subsequent sync reply wait, so
	// waiting on a reply never holds ingress capacity (the run slot bounds the
	// run). It is distinct from the acceptance token bucket and the run slot.
	// Ownership is exactly-once: released explicitly right after Admit returns
	// (below), with a guarded defer covering panics and the early error returns
	// between acquisition and Admit.
	ingressHeld := false
	releaseIngress := func() {
		if ingressHeld {
			ingressHeld = false
			s.webhookLimiter.releaseIngress(cand.EndpointID)
		}
	}
	if s.webhookLimiter != nil {
		if !s.webhookLimiter.acquireIngress(cand.EndpointID) {
			writeError(w, http.StatusTooManyRequests, "webhook ingress capacity exceeded")
			return
		}
		ingressHeld = true
		defer releaseIngress()
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

	// Bounded body read under the ingress slot: cap at 256 KiB via MaxBytesReader
	// and bound the read with a connection deadline, so a slow or oversized body
	// occupies only the ingress slot and never reaches the acceptance limiter, a
	// session, or a run.
	body, err := s.readWebhookBody(w, r, webhookBodyReadTimeout)
	if err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
		case isWebhookReadTimeout(err):
			writeError(w, http.StatusRequestTimeout, "request body read timed out")
		default:
			writeError(w, http.StatusBadRequest, "failed to read request body")
		}
		return
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}

	// Acceptance limiter: only a validly-shaped, bounded body consumes a token.
	if s.webhookLimiter != nil && !s.webhookLimiter.allow(cand.EndpointID) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
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

	// The ingress slot covered candidate resolution, the body read, and
	// synchronous admission only. Release it now — before compensation, response
	// mapping, and any sync reply wait — so a long wait never holds ingress
	// capacity. The run slot (reserved in the callback, released by done) still
	// bounds the admitted run.
	releaseIngress()

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

// readWebhookBody reads the request body under the 256 KiB MaxBytesReader cap and
// a bounded connection read deadline of timeout, clearing the deadline
// immediately after the read. The real *http.Server enforces the deadline; an
// httptest recorder reports http.ErrNotSupported, in which case the read proceeds
// without one. timeout is a parameter (the handler passes webhookBodyReadTimeout)
// so tests can drive this exact production path with a short deadline instead of
// reimplementing the algorithm.
func (s *Server) readWebhookBody(w http.ResponseWriter, r *http.Request, timeout time.Duration) ([]byte, error) {
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Now().Add(timeout)); err == nil {
		defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	} else if !errors.Is(err, http.ErrNotSupported) {
		return nil, err
	}
	return io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
}

// isWebhookReadTimeout reports whether err is a body-read deadline expiry.
func isWebhookReadTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
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
