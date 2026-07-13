// Package agentshadow bridges the legacy auth.PolicyEngine agent decision to
// the new revision-bound authz Authorizer, in shadow only.
//
// It is the single production wiring point that lets the new Authorizer observe
// the legacy agent read/execute decision without becoming a second authoritative
// decision. It translates the exact legacy AccessRequest into the typed authz
// vocabulary (Authority from the trusted auth.Subject adapter, a schema-validated
// Agent resource from the request's attributes and assignment), runs
// policy.Authorizer.ShadowCompare, and emits secret-free diagnostics on any
// mismatch or new-engine error. It never changes and never returns the legacy
// result.
//
// It lives outside internal/auth and internal/authz/policy on purpose: auth must
// not depend on the new policy store, and policy (a stdlib-only-leaf-plus-pgx
// implementation) must not import auth. This bridge is the only package that
// imports both, so the dependency direction stays acyclic.
//
// REMOVAL OWNER: this whole package is deleted when the Agent vertical cuts over
// in #709 — at that point the new Authorizer becomes the authoritative agent
// decision point, auth.PolicyEngine's agent path is removed, and the
// auth.AgentShadow seam goes with it. Recorded in foundation-baseline.md §11.
package agentshadow

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
)

// New-schema agent scope enum members (internal/authz/policy schema). The legacy
// config vocabulary is translated onto these before building the typed resource.
const (
	schemaScopeSystem = "system"
	schemaScopeUser   = "user"
	schemaScopeShared = "shared"
)

// DiagReason classifies why a shadow comparison produced a diagnostic.
type DiagReason string

const (
	// ReasonMismatch: the new decision disagreed with the legacy decision.
	ReasonMismatch DiagReason = "mismatch"
	// ReasonNewEngineError: the new Authorizer failed (Begin/Decide error) and
	// fell back to a fail-closed deny, regardless of whether that happened to
	// agree with the legacy result.
	ReasonNewEngineError DiagReason = "new_engine_error"
	// ReasonMalformedSubject: the legacy subject could not be mapped to a valid
	// Authority (missing user id, unknown role). No shadow decision was made.
	ReasonMalformedSubject DiagReason = "malformed_subject"
	// ReasonMalformedResource: the legacy resource attributes could not build a
	// schema-valid Agent resource (missing/typed-wrong scope, bad enum). No
	// shadow decision was made.
	ReasonMalformedResource DiagReason = "malformed_resource"
)

// Diagnostic is a structured, secret-free record of one shadow comparison that
// needs attention. It carries only identifiers, catalog values, and decision
// bits — never resource content or secrets — so it is safe to log or export as a
// metric.
type Diagnostic struct {
	Reason        DiagReason
	Action        string
	AgentID       string
	NewAllowed    bool
	LegacyAllowed bool
	Revision      int64
	PolicyID      string
	// Detail is a short, secret-free human-readable explanation.
	Detail string
}

// Sink receives shadow diagnostics for metrics/alerting. It runs on the caller's
// goroutine inside the legacy decision, so it must be cheap and non-blocking. A
// nil Sink is ignored.
type Sink func(ctx context.Context, d Diagnostic)

// Bridge implements auth.AgentShadow over a policy.Authorizer.
type Bridge struct {
	az   *policy.Authorizer
	log  *slog.Logger
	sink Sink
}

// Option configures a Bridge.
type Option func(*Bridge)

// WithLogger sets the structured logger used for diagnostics. Defaults to
// slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(b *Bridge) {
		if l != nil {
			b.log = l
		}
	}
}

// WithSink adds a metrics/alerting callback invoked on every diagnostic.
func WithSink(s Sink) Option {
	return func(b *Bridge) { b.sink = s }
}

// New builds a Bridge over an already-constructed Authorizer.
func New(az *policy.Authorizer, opts ...Option) *Bridge {
	b := &Bridge{az: az, log: slog.Default()}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// CompareAgentDecision runs the shadow comparison for one legacy agent
// read/execute decision. It is advisory: it emits diagnostics and returns
// nothing, so it can never alter the legacy allow/deny. The caller (the legacy
// engine) has already restricted this to ResourceAgent read/execute.
func (b *Bridge) CompareAgentDecision(ctx context.Context, req auth.AccessRequest, legacyAllowed bool) {
	var build func(agentID, ownerID, scope string, assigned bool) (authz.Request, error)
	switch req.Action {
	case auth.ActionRead:
		build = policy.AgentReadRequest
	case auth.ActionExecute:
		build = policy.AgentExecuteRequest
	default:
		// Defensive: the engine guard already excludes other actions. Do nothing
		// rather than shadowing a decision this bridge does not model.
		return
	}

	authority, err := req.Subject.Authority()
	if err != nil {
		b.emit(ctx, Diagnostic{
			Reason:        ReasonMalformedSubject,
			Action:        string(req.Action),
			AgentID:       req.Resource.ID,
			LegacyAllowed: legacyAllowed,
			Detail:        "legacy subject does not map to a valid authority: " + err.Error(),
		})
		return
	}

	rawScope, ok := stringAttr(req.Resource.Attrs, "scope")
	if !ok {
		b.emit(ctx, Diagnostic{
			Reason:        ReasonMalformedResource,
			Action:        string(req.Action),
			AgentID:       req.Resource.ID,
			LegacyAllowed: legacyAllowed,
			Detail:        "agent resource is missing a string scope attribute",
		})
		return
	}
	scope, ok := legacyScopeToSchema(rawScope)
	if !ok {
		b.emit(ctx, Diagnostic{
			Reason:        ReasonMalformedResource,
			Action:        string(req.Action),
			AgentID:       req.Resource.ID,
			LegacyAllowed: legacyAllowed,
			Detail:        "agent scope " + rawScope + " has no new-schema mapping",
		})
		return
	}

	// assigned mirrors the legacy `resource.id IN subject.agent_ids` condition
	// from the exact AccessRequest — the same fact the legacy assigned-agent
	// policy evaluates.
	assigned := slices.Contains(req.Subject.AgentIDs, req.Resource.ID)

	areq, err := build(req.Resource.ID, req.Resource.OwnerID, scope, assigned)
	if err != nil {
		b.emit(ctx, Diagnostic{
			Reason:        ReasonMalformedResource,
			Action:        string(req.Action),
			AgentID:       req.Resource.ID,
			LegacyAllowed: legacyAllowed,
			Detail:        "agent resource attributes failed schema validation: " + err.Error(),
		})
		return
	}

	res := b.az.ShadowCompare(ctx, authority, areq, legacyAllowed)

	// Report an erroring new engine even when it incidentally agrees: a new
	// engine that fails closed and only matches a legacy deny is not actually
	// deciding, and that must be visible before #709 cuts over to it.
	if res.Err != nil {
		d := Diagnostic{
			Reason:        ReasonNewEngineError,
			Action:        string(req.Action),
			AgentID:       req.Resource.ID,
			NewAllowed:    res.NewAllowed,
			LegacyAllowed: legacyAllowed,
			Revision:      res.Revision,
			Detail:        "new authorizer failed closed: " + res.Err.Error(),
		}
		// Request cancellation / deadline is expected operational noise, not a
		// shadow signal — the new engine simply never got to decide. Log it at
		// debug and never raise it to the WARN diagnostic stream or the
		// metrics/alert sink. Every genuine (non-context) error is preserved as a
		// warning diagnostic below.
		if isContextError(res.Err) {
			b.logExpected(ctx, d)
			return
		}
		b.emit(ctx, d)
		return
	}

	if !res.Match {
		b.emit(ctx, Diagnostic{
			Reason:        ReasonMismatch,
			Action:        string(req.Action),
			AgentID:       req.Resource.ID,
			NewAllowed:    res.NewAllowed,
			LegacyAllowed: legacyAllowed,
			Revision:      res.Revision,
			PolicyID:      res.PolicyID,
			Detail:        res.Diagnostic,
		})
	}
}

// emit logs the diagnostic at warning and forwards it to the sink. The legacy
// decision is already computed and returned by the caller, so emitting here
// cannot change the production result.
func (b *Bridge) emit(ctx context.Context, d Diagnostic) {
	b.log.WarnContext(ctx, "authz shadow diagnostic", b.logArgs(d)...)
	if b.sink != nil {
		b.sink(ctx, d)
	}
}

// logExpected records an expected, low-signal shadow skip (request cancellation)
// at debug only. It deliberately does NOT call the sink: metrics/alerting must
// not treat request cancellation as a shadow error.
func (b *Bridge) logExpected(ctx context.Context, d Diagnostic) {
	b.log.DebugContext(ctx, "authz shadow skipped: request canceled", b.logArgs(d)...)
}

// logArgs builds the secret-free structured fields shared by every diagnostic
// log line. It carries identifiers, catalog values, and decision bits only.
func (b *Bridge) logArgs(d Diagnostic) []any {
	return []any{
		"reason", string(d.Reason),
		"action", d.Action,
		"agent_id", d.AgentID,
		"new_allowed", d.NewAllowed,
		"legacy_allowed", d.LegacyAllowed,
		"revision", d.Revision,
		"policy_id", d.PolicyID,
		"detail", d.Detail,
	}
}

// isContextError reports whether err is (or wraps) a request-cancellation or
// deadline error. pgx propagates the context error through its wrapping, and
// Begin re-wraps with %w, so errors.Is walks the whole chain.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// legacyScopeToSchema maps the legacy agent scope vocabulary onto the new
// custom-policy schema enum. The legacy model has exactly two scopes —
// config.AgentScopeSystem ("system", all users) and config.AgentScopeRestricted
// ("restricted", assigned users only) — while the new Agent schema enum is
// system/user/shared. "system" is identical; "restricted" has no byte-identical
// new member and maps to "user" (assigned-only ≈ user-private). That mapping is
// exact for the active built-ins: only the "system" value is scope-sensitive, so
// a restricted agent is decided by the `assigned` fact and any non-"system" enum
// yields the same decision. A value already in the new vocabulary passes through;
// anything else is genuinely unmodeled and becomes a diagnostic rather than a
// silently-wrong fact.
//
// This translation is shadow-only and is deleted with this package when the Agent
// vertical owns one canonical scope vocabulary end to end (#709).
func legacyScopeToSchema(scope string) (string, bool) {
	switch scope {
	case config.AgentScopeSystem: // "system"
		return schemaScopeSystem, true
	case config.AgentScopeRestricted: // "restricted"
		return schemaScopeUser, true
	case schemaScopeUser, schemaScopeShared:
		// Already new-vocabulary (e.g. a future caller or a test): pass through.
		return scope, true
	default:
		return "", false
	}
}

// stringAttr reads a string-valued attribute from the legacy request's untyped
// attribute bag. A missing key or non-string value fails (ok=false) rather than
// coercing, so a malformed attribute becomes a diagnostic instead of a silently
// wrong shadow fact.
func stringAttr(attrs map[string]any, name string) (string, bool) {
	v, ok := attrs[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}
