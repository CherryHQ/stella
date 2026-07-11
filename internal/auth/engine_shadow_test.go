package auth

import (
	"context"
	"testing"
)

// fakeShadow records every CompareAgentDecision call so a test can assert
// exactly when (and with what) the engine invokes the shadow seam.
type fakeShadow struct {
	calls []shadowCall
}

type shadowCall struct {
	req    AccessRequest
	legacy bool
}

func (f *fakeShadow) CompareAgentDecision(_ context.Context, req AccessRequest, legacyAllowed bool) {
	f.calls = append(f.calls, shadowCall{req: req, legacy: legacyAllowed})
}

func agentReq(action Action, scope string, userID string, agentIDs ...string) AccessRequest {
	return AccessRequest{
		Subject: Subject{UserID: userID, Roles: []string{RoleUser}, AgentIDs: agentIDs},
		Action:  action,
		Resource: Resource{
			Type:  ResourceAgent,
			ID:    "a1",
			Attrs: map[string]any{"scope": scope},
		},
	}
}

// The shadow fires exactly once per agent read/execute decision, carrying the
// authoritative legacy result.
func TestEngineShadowFiresForAgentReadExecute(t *testing.T) {
	ctx := context.Background()
	fs := &fakeShadow{}
	engine := NewEngineFromPolicies(BuiltinPolicies(), WithAgentShadow(fs))

	// System-scoped agent read: legacy allows (user-system-agents).
	if !engine.Can(ctx, agentReq(ActionRead, "system", "u1")) {
		t.Fatal("expected legacy allow for system-agent read")
	}
	// Unassigned user-scoped agent execute: legacy denies (default deny).
	if engine.Can(ctx, agentReq(ActionExecute, "user", "u1")) {
		t.Fatal("expected legacy deny for unassigned user-agent execute")
	}

	if len(fs.calls) != 2 {
		t.Fatalf("shadow fired %d times, want 2", len(fs.calls))
	}
	if fs.calls[0].req.Action != ActionRead || !fs.calls[0].legacy {
		t.Fatalf("first shadow call = %+v, want read/legacy=true", fs.calls[0])
	}
	if fs.calls[1].req.Action != ActionExecute || fs.calls[1].legacy {
		t.Fatalf("second shadow call = %+v, want execute/legacy=false", fs.calls[1])
	}
}

// The shadow is scoped strictly to agent read/execute: no other resource and no
// other action reaches it, so inert verticals stay untouched.
func TestEngineShadowScopedToAgentReadExecute(t *testing.T) {
	ctx := context.Background()
	fs := &fakeShadow{}
	engine := NewEngineFromPolicies(BuiltinPolicies(), WithAgentShadow(fs))

	// Non-agent resource.
	engine.Can(ctx, AccessRequest{
		Subject:  Subject{UserID: "u1", Roles: []string{RoleUser}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceSession, ID: "s1"},
	})
	// Agent, but a non-shadowed action.
	engine.Can(ctx, agentReq(ActionCreate, "system", "u1"))
	engine.Can(ctx, agentReq(ActionDelete, "system", "u1"))
	// Collection-level agent_list read is a different resource type.
	engine.Can(ctx, AccessRequest{
		Subject:  Subject{UserID: "u1", Roles: []string{RoleUser}},
		Action:   ActionRead,
		Resource: Resource{Type: ResourceAgentList},
	})

	if len(fs.calls) != 0 {
		t.Fatalf("shadow fired %d times for out-of-scope requests, want 0", len(fs.calls))
	}
}

// A shadow injection must never change the legacy decision. The engine with a
// shadow returns exactly what the same engine without one returns.
func TestEngineShadowDoesNotAlterDecision(t *testing.T) {
	ctx := context.Background()
	plain := NewEngineFromPolicies(BuiltinPolicies())
	shadowed := NewEngineFromPolicies(BuiltinPolicies(), WithAgentShadow(&fakeShadow{}))

	cases := []AccessRequest{
		agentReq(ActionRead, "system", "u1"),
		agentReq(ActionExecute, "user", "u1"),          // unassigned -> deny
		agentReq(ActionRead, "user", "u1", "a1"),       // assigned -> allow
		agentReq(ActionExecute, "user", "u1", "other"), // assigned to a different agent -> deny
	}
	for i, req := range cases {
		if got, want := shadowed.Can(ctx, req), plain.Can(ctx, req); got != want {
			t.Fatalf("case %d: shadowed decision %v != plain decision %v", i, got, want)
		}
	}
}

// A nil shadow (the default) is a safe no-op.
func TestEngineNilShadowNoop(t *testing.T) {
	ctx := context.Background()
	engine := NewEngineFromPolicies(BuiltinPolicies())
	if !engine.Can(ctx, agentReq(ActionRead, "system", "u1")) {
		t.Fatal("nil-shadow engine should still decide the legacy result")
	}
}

// panicShadow models a broken AgentShadow (or its sink/logger internals) that
// panics on every call.
type panicShadow struct{ calls int }

func (p *panicShadow) CompareAgentDecision(context.Context, AccessRequest, bool) {
	p.calls++
	panic("boom: shadow/sink/logger internal fault")
}

// A panic anywhere in the shadow must be recovered at the engine boundary: Can
// returns the already-computed legacy decision unchanged (both on allow and on
// deny) and the process keeps going.
func TestEngineShadowPanicRecoveredPreservesLegacyDecision(t *testing.T) {
	ctx := context.Background()
	ps := &panicShadow{}
	engine := NewEngineFromPolicies(BuiltinPolicies(), WithAgentShadow(ps))

	// Legacy ALLOW: system-scoped agent read.
	allowReq := agentReq(ActionRead, "system", "u1")
	if got := engine.Can(ctx, allowReq); !got {
		t.Fatalf("panic changed a legacy allow to %v", got)
	}

	// Legacy DENY: unassigned user-scoped agent execute.
	denyReq := agentReq(ActionExecute, "user", "u1")
	if got := engine.Can(ctx, denyReq); got {
		t.Fatalf("panic changed a legacy deny to %v", got)
	}

	// The panicking shadow was actually invoked for both decisions (the guard did
	// not skip it), and the process continued to run a third decision cleanly.
	if ps.calls != 2 {
		t.Fatalf("shadow invoked %d times, want 2", ps.calls)
	}
	if !engine.Can(ctx, allowReq) {
		t.Fatal("engine stopped deciding after a recovered shadow panic")
	}
}

// A recovered panic must not change the decision relative to an engine with no
// shadow at all.
func TestEngineShadowPanicMatchesUnshadowedDecision(t *testing.T) {
	ctx := context.Background()
	plain := NewEngineFromPolicies(BuiltinPolicies())
	panicky := NewEngineFromPolicies(BuiltinPolicies(), WithAgentShadow(&panicShadow{}))

	cases := []AccessRequest{
		agentReq(ActionRead, "system", "u1"),           // allow
		agentReq(ActionExecute, "restricted", "u1"),    // unassigned -> deny
		agentReq(ActionRead, "restricted", "u1", "a1"), // assigned -> allow
	}
	for i, req := range cases {
		if got, want := panicky.Can(ctx, req), plain.Can(ctx, req); got != want {
			t.Fatalf("case %d: panicking-shadow decision %v != plain decision %v", i, got, want)
		}
	}
}
