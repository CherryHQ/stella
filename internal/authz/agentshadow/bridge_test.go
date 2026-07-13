package agentshadow_test

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/agentshadow"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// capture records diagnostics emitted by the bridge sink.
type capture struct{ diags []agentshadow.Diagnostic }

func (c *capture) sink(_ context.Context, d agentshadow.Diagnostic) {
	c.diags = append(c.diags, d)
}

// agentReq builds the exact legacy AccessRequest shape produced by
// server.agentReadRequest / channel.ResolveAgent for an agent read/execute.
func agentReq(action auth.Action, userID, role, scope, agentID string, assignedIDs ...string) auth.AccessRequest {
	return auth.AccessRequest{
		Subject: auth.Subject{UserID: userID, Roles: []string{role}, AgentIDs: assignedIDs},
		Action:  action,
		Resource: auth.Resource{
			Type:  auth.ResourceAgent,
			ID:    agentID,
			Attrs: map[string]any{"scope": scope},
		},
	}
}

// The supported legacy agent cases must produce ZERO parity mismatch: for every
// admin / system-agent / assigned / unassigned case across read and execute, the
// new Authorizer agrees with the legacy engine, so the bridge stays silent.
func TestBridgeZeroParityMismatchForSupportedCases(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	az := policy.New(pool)
	legacy := auth.NewEngineFromPolicies(auth.BuiltinPolicies())

	cases := []struct {
		name string
		req  auth.AccessRequest
	}{
		// Restricted agents carry the REAL legacy scope value "restricted" (not a
		// new-vocabulary value): the bridge must translate it to the new schema
		// enum, otherwise every restricted agent would falsely diagnose.
		{"admin-read-restricted", agentReq(auth.ActionRead, "admin1", auth.RoleAdmin, "restricted", "a1")},
		{"admin-execute-restricted", agentReq(auth.ActionExecute, "admin1", auth.RoleAdmin, "restricted", "a1")},
		{"user-read-system", agentReq(auth.ActionRead, "u1", auth.RoleUser, "system", "a1")},
		{"user-execute-system", agentReq(auth.ActionExecute, "u1", auth.RoleUser, "system", "a1")},
		{"user-read-assigned", agentReq(auth.ActionRead, "u1", auth.RoleUser, "restricted", "a1", "a1")},
		{"user-execute-assigned", agentReq(auth.ActionExecute, "u1", auth.RoleUser, "restricted", "a1", "a1")},
		{"user-read-unassigned", agentReq(auth.ActionRead, "u1", auth.RoleUser, "restricted", "a1")},
		{"user-execute-unassigned", agentReq(auth.ActionExecute, "u1", auth.RoleUser, "restricted", "a1", "other")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap := &capture{}
			bridge := agentshadow.New(az, agentshadow.WithSink(cap.sink))
			legacyAllowed := legacy.Can(ctx, tc.req)
			bridge.CompareAgentDecision(ctx, tc.req, legacyAllowed)
			if len(cap.diags) != 0 {
				t.Fatalf("unexpected parity diagnostic(s) for %s (legacy=%v): %+v", tc.name, legacyAllowed, cap.diags)
			}
		})
	}
}

// A deliberate disagreement (legacy result contradicting the new decision) must
// surface a structured mismatch diagnostic — and nothing else changes.
func TestBridgeEmitsMismatchDiagnostic(t *testing.T) {
	ctx := context.Background()
	az := policy.New(dbtest.New(t))
	cap := &capture{}
	bridge := agentshadow.New(az, agentshadow.WithSink(cap.sink))

	// New engine allows a user reading a system agent; feed a contradicting
	// legacy deny to force a mismatch.
	req := agentReq(auth.ActionRead, "u1", auth.RoleUser, "system", "a1")
	bridge.CompareAgentDecision(ctx, req, false)

	if len(cap.diags) != 1 {
		t.Fatalf("want exactly 1 diagnostic, got %d: %+v", len(cap.diags), cap.diags)
	}
	d := cap.diags[0]
	if d.Reason != agentshadow.ReasonMismatch {
		t.Fatalf("reason = %q, want mismatch", d.Reason)
	}
	if !d.NewAllowed || d.LegacyAllowed {
		t.Fatalf("diagnostic decision bits = new:%v legacy:%v, want new:true legacy:false", d.NewAllowed, d.LegacyAllowed)
	}
	if d.AgentID != "a1" || d.Action != string(auth.ActionRead) || d.Detail == "" {
		t.Fatalf("diagnostic missing structured fields: %+v", d)
	}
}

// A malformed resource (no scope attribute) produces a diagnostic and makes no
// shadow decision — it never touches the legacy result.
func TestBridgeMalformedResourceDiagnostic(t *testing.T) {
	ctx := context.Background()
	az := policy.New(dbtest.New(t))
	cap := &capture{}
	bridge := agentshadow.New(az, agentshadow.WithSink(cap.sink))

	req := auth.AccessRequest{
		Subject:  auth.Subject{UserID: "u1", Roles: []string{auth.RoleUser}},
		Action:   auth.ActionRead,
		Resource: auth.Resource{Type: auth.ResourceAgent, ID: "a1"}, // no scope attr
	}
	bridge.CompareAgentDecision(ctx, req, true)

	if len(cap.diags) != 1 || cap.diags[0].Reason != agentshadow.ReasonMalformedResource {
		t.Fatalf("want one malformed_resource diagnostic, got %+v", cap.diags)
	}
}

// A scope value the new schema cannot model (neither legacy "system"/"restricted"
// nor a new-vocabulary value) is surfaced as a diagnostic, never a wrong fact.
func TestBridgeUnmappedScopeDiagnostic(t *testing.T) {
	ctx := context.Background()
	az := policy.New(dbtest.New(t))
	cap := &capture{}
	bridge := agentshadow.New(az, agentshadow.WithSink(cap.sink))

	req := agentReq(auth.ActionRead, "u1", auth.RoleUser, "totally-unknown-scope", "a1")
	bridge.CompareAgentDecision(ctx, req, true)
	if len(cap.diags) != 1 || cap.diags[0].Reason != agentshadow.ReasonMalformedResource {
		t.Fatalf("want one malformed_resource diagnostic for an unmapped scope, got %+v", cap.diags)
	}
}

// A malformed subject (empty user id / unknown role) cannot map to an Authority
// and yields a diagnostic without a shadow decision.
func TestBridgeMalformedSubjectDiagnostic(t *testing.T) {
	ctx := context.Background()
	az := policy.New(dbtest.New(t))

	t.Run("empty-user", func(t *testing.T) {
		cap := &capture{}
		bridge := agentshadow.New(az, agentshadow.WithSink(cap.sink))
		req := agentReq(auth.ActionRead, "", auth.RoleUser, "system", "a1")
		bridge.CompareAgentDecision(ctx, req, true)
		if len(cap.diags) != 1 || cap.diags[0].Reason != agentshadow.ReasonMalformedSubject {
			t.Fatalf("want one malformed_subject diagnostic, got %+v", cap.diags)
		}
	})

	t.Run("unknown-role", func(t *testing.T) {
		cap := &capture{}
		bridge := agentshadow.New(az, agentshadow.WithSink(cap.sink))
		req := agentReq(auth.ActionRead, "u1", "superuser", "system", "a1")
		bridge.CompareAgentDecision(ctx, req, true)
		if len(cap.diags) != 1 || cap.diags[0].Reason != agentshadow.ReasonMalformedSubject {
			t.Fatalf("want one malformed_subject diagnostic, got %+v", cap.diags)
		}
	})
}

// When the new Authorizer is unavailable it fails closed; the bridge reports a
// new_engine_error diagnostic (even if that fail-closed deny would agree with a
// legacy deny — an erroring engine is a shadow signal in its own right).
func TestBridgeReportsNewEngineError(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	az := policy.New(pool)
	pool.Close() // Begin's revision read now fails

	cap := &capture{}
	bridge := agentshadow.New(az, agentshadow.WithSink(cap.sink))
	req := agentReq(auth.ActionRead, "u1", auth.RoleUser, "system", "a1")

	// Legacy allowed: unavailable new engine is both a mismatch and an error;
	// the error classification wins.
	bridge.CompareAgentDecision(ctx, req, true)
	if len(cap.diags) != 1 || cap.diags[0].Reason != agentshadow.ReasonNewEngineError {
		t.Fatalf("want one new_engine_error diagnostic, got %+v", cap.diags)
	}

	// Legacy denied: the fail-closed deny agrees, but the error is still surfaced.
	cap.diags = nil
	bridge.CompareAgentDecision(ctx, req, false)
	if len(cap.diags) != 1 || cap.diags[0].Reason != agentshadow.ReasonNewEngineError {
		t.Fatalf("erroring-but-agreeing engine must still diagnose, got %+v", cap.diags)
	}
}

// Request cancellation is expected operational noise: when the new engine cannot
// read its revision because the request context is already canceled, the bridge
// must NOT raise a warning diagnostic to the metrics/alert sink. This is the
// deliberate contrast to TestBridgeReportsNewEngineError (a genuine failure,
// which DOES emit).
func TestBridgeSuppressesContextCancellationNoise(t *testing.T) {
	pool := dbtest.New(t)
	az := policy.New(pool)
	cap := &capture{}
	bridge := agentshadow.New(az, agentshadow.WithSink(cap.sink))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the request is already gone

	req := agentReq(auth.ActionRead, "u1", auth.RoleUser, "system", "a1")
	// Legacy allowed: the new engine fails closed under the canceled context, but
	// that failure is cancellation noise and stays out of the sink.
	bridge.CompareAgentDecision(ctx, req, true)
	if len(cap.diags) != 0 {
		t.Fatalf("request cancellation must be suppressed from the sink, got %+v", cap.diags)
	}
}

// REQUIREMENT: custom-policy activation is shadow-only. A custom deny policy can
// flip the NEW decision (and therefore raise a diagnostic mismatch), but it must
// NOT change the legacy production result returned by the wired engine.
func TestCustomPolicyIsShadowOnly(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	az := policy.New(pool)
	svc := policy.NewService(az)

	cap := &capture{}
	bridge := agentshadow.New(az, agentshadow.WithSink(cap.sink))
	engine := auth.NewEngineFromPolicies(auth.BuiltinPolicies(), auth.WithAgentShadow(bridge))

	req := agentReq(auth.ActionRead, "u1", auth.RoleUser, "system", "a1")

	// Before any custom policy: legacy allows, new agrees, no diagnostic.
	if !engine.Can(ctx, req) {
		t.Fatal("legacy must allow a user reading a system agent")
	}
	if len(cap.diags) != 0 {
		t.Fatalf("no diagnostic expected before custom policy, got %+v", cap.diags)
	}

	// Activate a custom policy that DENIES the system-agent read in the new engine.
	if _, _, err := svc.CreatePolicy(ctx, policy.PolicyInput{
		Name:       "deny system agent read",
		Resource:   authz.ResourceAgent,
		Action:     authz.ActionRead,
		Effect:     policy.EffectDeny,
		Subjects:   policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
		Predicates: []policy.Predicate{policy.Eq("scope", "system")},
	}); err != nil {
		t.Fatalf("create custom deny policy: %v", err)
	}

	cap.diags = nil
	// Legacy result is UNCHANGED — the custom policy only lives in the new engine.
	if !engine.Can(ctx, req) {
		t.Fatal("custom policy changed the production result; it must be shadow-only")
	}
	// ...but the new engine now denies, so the shadow raises a mismatch.
	if len(cap.diags) != 1 || cap.diags[0].Reason != agentshadow.ReasonMismatch {
		t.Fatalf("want one mismatch diagnostic after custom deny, got %+v", cap.diags)
	}
	if cap.diags[0].NewAllowed || !cap.diags[0].LegacyAllowed {
		t.Fatalf("diagnostic bits = new:%v legacy:%v, want new:false legacy:true", cap.diags[0].NewAllowed, cap.diags[0].LegacyAllowed)
	}
}

// Agent list parity is covered at the ShadowCompare unit level in
// internal/authz/policy (TestShadowCompareAgentListParity): the legacy list
// decision uses the agent_list resource type, which the bridge deliberately does
// not shadow (the engine guard only fires for ResourceAgent read/execute), so
// there is no bridge list path to exercise here.
