package policy

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
)

// Shadow comparison.
//
// While this subphase is shadow-only, the new Authorizer must be observable
// against the legacy production decision (auth.PolicyEngine) without becoming a
// second authoritative decision. ShadowCompare runs the new decision and diffs
// it against a legacy result the caller supplies, returning structured
// mismatch diagnostics. It deliberately does NOT compute the legacy decision
// itself (that would pull internal/auth into this package and blur the
// dependency direction) — the future agent PEP passes in its existing
// engine.Can result.
//
// REMOVAL OWNER: this shadow surface (and the whole shadow-only mode) is
// retired by Stack 4 / #709 (Agent + Session vertical cutover), when the
// Authorizer becomes the authoritative agent decision point and the legacy
// PolicyEngine agent path is deleted. It must not be wired into a production
// callsite before then, because doing so would make two engines authoritative
// for the same agent decision.

// ShadowResult is the structured outcome of comparing the new Authorizer's
// decision with the legacy decision for one request.
type ShadowResult struct {
	// Match is true when the new decision agrees with the legacy decision.
	Match bool
	// NewAllowed is the new Authorizer's decision.
	NewAllowed bool
	// LegacyAllowed is the legacy decision supplied by the caller.
	LegacyAllowed bool
	// Revision is the policy revision the new decision was bound to.
	Revision int64
	// Visibility and PolicyID describe the new decision for diagnostics.
	Visibility authz.Visibility
	PolicyID   string
	// Diagnostic is a human-readable explanation, populated on a mismatch.
	Diagnostic string
	// Err is the Begin/Decide failure that forced the new decision to fail
	// closed, or nil on a clean decision. It is reported independently of Match:
	// a new engine that errors but happens to agree with a legacy deny is still a
	// shadow signal worth surfacing (the new engine is not actually deciding), so
	// callers emit a diagnostic whenever Err != nil even if Match is true.
	Err error
}

// ShadowCompare binds an Evaluation, decides the request, and diffs the result
// against legacyAllowed. A Begin/Decide failure fails closed (treated as a new
// deny) and is reported as a mismatch when the legacy path allowed. It never
// affects production state.
func (az *Authorizer) ShadowCompare(
	ctx context.Context,
	authority authz.Authority,
	req authz.Request,
	legacyAllowed bool,
) ShadowResult {
	res := ShadowResult{LegacyAllowed: legacyAllowed, Revision: -1}

	eval, err := az.Begin(ctx, authority)
	if err != nil {
		res.NewAllowed = false
		res.Err = err
		res.Match = !legacyAllowed
		if !res.Match {
			res.Diagnostic = fmt.Sprintf("new authorizer unavailable (%v) but legacy allowed", err)
		}
		return res
	}
	res.Revision = eval.Revision()

	dec, err := eval.Decide(req)
	if err != nil {
		res.NewAllowed = false
		res.Err = err
		res.Match = !legacyAllowed
		if !res.Match {
			res.Diagnostic = fmt.Sprintf("new decide error (%v) but legacy allowed", err)
		}
		return res
	}

	res.NewAllowed = dec.Allowed()
	res.Visibility = dec.Visibility()
	res.PolicyID = dec.PolicyID()
	res.Match = res.NewAllowed == legacyAllowed
	if !res.Match {
		res.Diagnostic = fmt.Sprintf(
			"decision mismatch on %s %s(id=%q owner=%q): new=%v legacy=%v policy=%q revision=%d",
			req.Action(), req.Resource().Type(), req.Resource().ID(), req.Resource().OwnerID(),
			res.NewAllowed, legacyAllowed, res.PolicyID, res.Revision,
		)
	}
	return res
}

// AgentReadRequest builds the typed request the shadow path uses to mirror the
// legacy agent read decision. scope is the agent's scope; assigned reports
// whether the acting user has the agent assigned.
func AgentReadRequest(agentID, ownerID, scope string, assigned bool) (authz.Request, error) {
	return agentRequest(authz.ActionRead, agentID, ownerID, scope, assigned)
}

// AgentExecuteRequest is AgentReadRequest for the execute action.
func AgentExecuteRequest(agentID, ownerID, scope string, assigned bool) (authz.Request, error) {
	return agentRequest(authz.ActionExecute, agentID, ownerID, scope, assigned)
}

// AgentListRequest builds the collection-level agent list request, mirroring the
// legacy `agent_list` read decision (any authenticated user may enumerate the
// agent catalog; per-agent read is filtered separately). It carries no per-agent
// id or attributes.
func AgentListRequest() (authz.Request, error) {
	res, err := authz.NewResource(authz.ResourceAgent, "", "")
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(authz.ActionList, res, authz.InvocationFacts{})
}

func agentRequest(action authz.Action, agentID, ownerID, scope string, assigned bool) (authz.Request, error) {
	res, err := AgentResource(agentID, ownerID, scope, assigned)
	if err != nil {
		return authz.Request{}, err
	}
	return authz.NewRequest(action, res, authz.InvocationFacts{})
}
