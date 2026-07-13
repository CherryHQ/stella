package policy

import (
	"github.com/CherryHQ/stella/internal/authz"
)

// snapshot is an immutable, revision-bound compiled policy set. It is compiled
// once (built-ins + active custom rows) and published atomically; an Evaluation
// holds a pointer to one and never mutates it, so every Decide in a use case
// observes the same policy view without another database read.
type snapshot struct {
	revision int64
	policies []compiledPolicy // built-ins first, then active custom rows (priority-ordered)
}

// evaluation is the concrete authz.Evaluation. It binds one authority to one
// immutable snapshot. Decide is pure in-memory: no database read, no lock, no
// transaction held for the lifetime of the use case.
type evaluation struct {
	authority authz.Authority
	snap      *snapshot
}

// Revision returns the policy revision this evaluation is bound to.
func (e *evaluation) Revision() int64 { return e.snap.revision }

// Decide answers one typed request against the bound revision using default-deny
// with deny-overrides: any matching deny rule denies; otherwise a matching allow
// rule allows; otherwise the default deny applies.
func (e *evaluation) Decide(req authz.Request) (authz.Decision, error) {
	if !req.Action().Valid() || !req.Resource().Valid() {
		// A malformed request cannot be authorized; fail closed rather than
		// scanning policies against an invalid catalog value.
		return authz.Deny(authz.VisibilityHidden, "", e.audit(req, false, "", "invalid request")), authz.ErrInvalidRequest
	}

	var (
		matchedAllow bool
		allowID      string
	)
	for i := range e.snap.policies {
		p := e.snap.policies[i]
		if !p.matches(e.authority, req) {
			continue
		}
		if p.effect == effectDeny {
			// Deny overrides: the first matching deny is decisive regardless of
			// any allow, so we can stop here.
			return authz.Deny(p.allowed, p.id, e.audit(req, false, p.id, "deny policy")), nil
		}
		if !matchedAllow {
			matchedAllow = true
			allowID = p.id
		}
	}
	if matchedAllow {
		return authz.Allow(allowID, e.audit(req, true, allowID, "allow policy")), nil
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
