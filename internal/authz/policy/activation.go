package policy

import "github.com/CherryHQ/stella/internal/authz"

// activation is the resource-activation catalog: which catalog resources may
// carry custom policies in this subphase, and in what mode. It is in-code, not
// operator data — activation is a property of which migration stack owns a
// resource, so it belongs in the code that owns the cutover, not a mutable row.
//
// Shadow-only rule for Stack 2 / #707 B: the ONE existing production resource
// (Agent) is shadow-enabled — it accepts typed custom policies and is evaluated
// by the new Authorizer, but the Authorizer is not yet wired into any production
// decision path, so it is not authoritative. EVERY other resource is inactive:
// a custom-policy write is rejected (fail closed) and any pre-existing row is
// quarantined, until its owning stack cuts the resource over.
type activation uint8

const (
	// activeInactive means no custom policy may be written and none is evaluated.
	activeInactive activation = iota
	// activeShadow means custom policies are accepted and evaluated, but the
	// Authorizer is not yet the production decision point for the resource.
	activeShadow
)

// activationCatalog maps each resource to its activation mode. Anything absent
// is inactive by default (fail closed).
//
// System catalog and public/tool entries are explicitly inactive here: they are
// authenticated read-only reference data (ResourceSystemCatalog) or pure public
// capabilities (ResourceTool) whose custom-policy story is owned by the
// platform/control-plane stack (Stack 7). They may permit custom policy later,
// but not in this subphase, so they reject writes and quarantine existing rows
// exactly like every other not-yet-cut-over resource.
var activationCatalog = map[authz.ResourceType]activation{
	authz.ResourceAgent: activeShadow,
}

func activationFor(rt authz.ResourceType) activation {
	if a, ok := activationCatalog[rt]; ok {
		return a
	}
	return activeInactive
}

// resourceAcceptsCustomPolicy reports whether a resource may currently receive a
// custom-policy write. Only shadow-enabled resources do.
func resourceAcceptsCustomPolicy(rt authz.ResourceType) bool {
	return activationFor(rt) == activeShadow
}
