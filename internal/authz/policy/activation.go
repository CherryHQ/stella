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
	// activeActive means the Authorizer is the authoritative production decision
	// point for the resource: custom policies are accepted and evaluated, and the
	// resource's PEP consults this Authorizer with no legacy engine behind it.
	activeActive
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
	// #709 cut the Agent vertical over: the Authorizer is the authoritative agent
	// decision point (the legacy PolicyEngine agent path and the former shadow bridge
	// are deleted), so Agent is fully active rather than shadow.
	authz.ResourceAgent: activeActive,
	// #709: Session and Workspace are enforced only through
	// internal/agent/session/access.Service, including custom policy facts.
	authz.ResourceSession:   activeActive,
	authz.ResourceWorkspace: activeActive,
	// #710: Workflow is enforced only through internal/workflow.Service's
	// Authority-based Access PEP (the legacy As(authz.Identity) facade is gone).
	authz.ResourceWorkflow: activeActive,
	// #710: Scheduler jobs are enforced through internal/scheduler.Service's
	// Authority-based Access PEP. User and system fires are re-decided at the
	// unified dispatch boundary; plugin listener jobs remain an explicit host path.
	authz.ResourceScheduler: activeActive,
	// #710: Goals are enforced through internal/goal.Service's Authority-based
	// Access PEP; each durable attempt reconstructs owner/executor authority and
	// re-decides the Goal plus actual Agent before running.
	authz.ResourceGoal: activeActive,
	// #710: Skills are enforced only through internal/skillaccess.Service's
	// Authority-based Access PEP. HTTP transports, the agent skills tool (via the
	// skills read port), and the reflect reviewer/curator all decide every
	// DB-backed skill read and write against it; only filesystem project/built-in
	// skills are exempt (they are not DB rows).
	authz.ResourceSkill: activeActive,
	// #711: Vault entries are enforced only through internal/vault.Service's
	// Authority-based Access PEP. user/user_agent scopes are user-owned (with an
	// agent-read gate folded in); system/system_agent scopes are reachable only via
	// admin-full-access. Trusted host-side callers use the raw Service methods.
	//
	// Email, Connection, Share, and Recally are deliberately NOT policy-backed: they
	// are Authority-bound user capabilities enforced by their own domain Access
	// services plus user-scoped durable queries (see internal/{email,connections,
	// share,recally}). Their catalog entries stay inactive so a stray custom-policy
	// write for them fails closed.
	authz.ResourceVault: activeActive,
	// #712: the deployment control-plane resources are enforced only through
	// internal/controlplane.Service's Authority-based Access PEP. They are
	// administered, not user-owned: the built-in admin-full-access policy is the
	// sole grant, so a non-admin actor is default-denied exactly as the legacy
	// requireAdmin gate was. Activating them lets an operator additionally author a
	// custom policy along the kind/status/owner facts.
	authz.ResourceProvider: activeActive,
	authz.ResourceSettings: activeActive,
	authz.ResourcePlugin:   activeActive,
	authz.ResourceChannel:  activeActive,
}

func activationFor(rt authz.ResourceType) activation {
	if a, ok := activationCatalog[rt]; ok {
		return a
	}
	return activeInactive
}

// resourceAcceptsCustomPolicy reports whether a resource may currently receive a
// custom-policy write. Both shadow-enabled and authoritatively-active resources
// do; only fully-inactive resources reject writes and quarantine existing rows.
func resourceAcceptsCustomPolicy(rt authz.ResourceType) bool {
	switch activationFor(rt) {
	case activeShadow, activeActive:
		return true
	default:
		return false
	}
}
