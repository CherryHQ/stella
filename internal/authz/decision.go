package authz

import "errors"

// This file defines the typed request/decision shapes an Authorizer evaluates.
// They are pure values with validating constructors; the Authorizer/Evaluation
// interfaces that consume them live in authorizer.go and are implemented in a
// later subphase (internal/authz/policy).

var (
	// ErrInvalidResource is returned when a resource reference names an unknown
	// resource type.
	ErrInvalidResource = errors.New("authz: invalid resource")
	// ErrInvalidRequest is returned when a request names an invalid action or
	// resource.
	ErrInvalidRequest = errors.New("authz: invalid request")
)

// Resource is a typed reference to the thing an action targets. Its attributes
// originate from typed domain builders, never a transport-supplied attribute
// bag. For this contract layer it carries the catalog type plus identity and
// ownership; richer per-domain attribute schemas attach in the domain builders
// that produce a Resource in subphase B.
type Resource struct {
	typ     ResourceType
	id      string
	ownerID string
}

// NewResource constructs a validated resource reference. The type must be a
// catalog member. A collection-level request (list) has an empty id.
func NewResource(typ ResourceType, id, ownerID string) (Resource, error) {
	if !typ.Valid() {
		return Resource{}, ErrInvalidResource
	}
	return Resource{typ: typ, id: id, ownerID: ownerID}, nil
}

// Type returns the resource's catalog type.
func (r Resource) Type() ResourceType { return r.typ }

// ID returns the resource id (empty for collection-level requests).
func (r Resource) ID() string { return r.id }

// OwnerID returns the resource owner id, if known.
func (r Resource) OwnerID() string { return r.ownerID }

// Valid reports whether the resource references a catalog type.
func (r Resource) Valid() bool { return r.typ.Valid() }

// Request is one authorization question: may the bound Authority perform Action
// on Resource, given these invocation facts. Authority is not part of Request —
// it is bound once by Authorizer.Begin — so a request cannot re-assert identity.
type Request struct {
	action   Action
	resource Resource
	facts    InvocationFacts
}

// NewRequest constructs a validated request. Action must be a catalog member and
// resource must be valid.
func NewRequest(action Action, resource Resource, facts InvocationFacts) (Request, error) {
	if !action.Valid() || !resource.Valid() {
		return Request{}, ErrInvalidRequest
	}
	return Request{action: action, resource: resource, facts: facts}, nil
}

// Action returns the requested action.
func (r Request) Action() Action { return r.action }

// Resource returns the target resource.
func (r Request) Resource() Resource { return r.resource }

// Facts returns the invocation facts.
func (r Request) Facts() InvocationFacts { return r.facts }

// Decision is the typed outcome of evaluating a Request. The zero Decision is a
// deny with no visibility — it fails closed, so a decision that was never
// populated by a policy never accidentally allows. Visibility controls whether a
// denial surfaces as forbidden (403) or hidden (404); it is only meaningful on a
// deny.
type Decision struct {
	allowed    bool
	visibility Visibility
	policyID   string
	audit      AuditRecord
}

// Allow constructs an allow decision, optionally carrying the deciding policy id
// and structured audit metadata.
func Allow(policyID string, audit AuditRecord) Decision {
	return Decision{allowed: true, policyID: policyID, audit: audit}
}

// Deny constructs a deny decision with the visibility rule to apply and audit
// metadata. An invalid visibility is coerced to Hidden (the safest surface:
// reveal nothing) so a malformed deny still fails closed.
func Deny(visibility Visibility, policyID string, audit AuditRecord) Decision {
	if !visibility.Valid() {
		visibility = VisibilityHidden
	}
	return Decision{allowed: false, visibility: visibility, policyID: policyID, audit: audit}
}

// Allowed reports whether access is granted.
func (d Decision) Allowed() bool { return d.allowed }

// Visibility returns the denial-visibility rule (meaningful only when denied).
func (d Decision) Visibility() Visibility { return d.visibility }

// PolicyID returns the id of the deciding policy, if any.
func (d Decision) PolicyID() string { return d.policyID }

// Audit returns the structured audit metadata for the decision.
func (d Decision) Audit() AuditRecord { return d.audit }

// AuditRecord is structured, secret-free audit metadata about a decision. It
// records who/what/which without ever embedding resource content or secrets, so
// it is safe to log or emit. All fields are optional; a zero AuditRecord is a
// valid "no metadata" record.
type AuditRecord struct {
	ActorKind  ActorKind
	Action     Action
	Resource   ResourceType
	ResourceID string
	Allowed    bool
	PolicyID   string
	Revision   int64
	Reason     string
}
