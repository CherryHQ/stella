package policy

import "github.com/CherryHQ/stella/internal/authz"

const staticRevision int64 = 0

// snapshot is the immutable built-in policy set shared by every evaluation.
type snapshot struct {
	revision int64
	policies []compiledPolicy
}

var staticSnapshot = &snapshot{revision: staticRevision, policies: builtinPolicies()}

// evaluation binds one valid Authority to the fixed built-in rules. Decide is
// pure in-memory and every decision in a use case sees the same rule set.
type evaluation struct {
	authority authz.Authority
	snap      *snapshot
}

// Revision is retained by the temporary authz.Evaluation interface. Static
// rules never change at runtime, so every evaluation has the same revision.
func (e *evaluation) Revision() int64 { return e.snap.revision }

// Decide answers one typed request using the fixed rules and default deny.
func (e *evaluation) Decide(req authz.Request) (authz.Decision, error) {
	if !req.Action().Valid() || !req.Resource().Valid() {
		return authz.Deny(authz.VisibilityHidden, "", e.audit(req, false, "", "invalid request")), authz.ErrInvalidRequest
	}

	for _, rule := range e.snap.policies {
		if rule.matches(e.authority, req) {
			return authz.Allow(rule.id, e.audit(req, true, rule.id, "allow policy")), nil
		}
	}
	return authz.Deny(authz.VisibilityForbidden, "", e.audit(req, false, "", "default deny")), nil
}

func (e *evaluation) audit(req authz.Request, allowed bool, policyID, reason string) authz.AuditRecord {
	return authz.AuditRecord{
		ActorKind:  e.authority.Kind(),
		Action:     req.Action(),
		Resource:   req.Resource().Type(),
		ResourceID: req.Resource().ID(),
		Allowed:    allowed,
		PolicyID:   policyID,
		Revision:   e.snap.revision,
		Reason:     reason,
	}
}
