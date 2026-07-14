package policy

import "github.com/CherryHQ/stella/internal/authz"

// activation is the resource-activation catalog: which catalog resources may
// carry custom policies, and in what mode. It is in-code, not operator data —
// activation is a property of which migration stack owns a resource, so it
// belongs in the code that owns the cutover, not a mutable row.
//
// The catalog is now TOTAL: every authz.AllResourceTypes() member has an
// explicit entry (enforced by TestActivationCatalogIsTotal), so every protected
// resource carries a deliberate activation decision rather than falling through
// to a silent default. The eight policy-backed resources are active. The remaining 15
// are deliberately inactive: they are protected, just not by the custom-policy
// Authorizer, each by a named dedicated mechanism spelled out at its entry. An
// inactive resource rejects custom-policy writes (fail closed) and any
// pre-existing row is quarantined.
//
// #712 is the FINAL stack: there is no future owner that will flip an inactive
// resource to active, so an inactive entry is a permanent design decision, not a
// not-yet-cut-over placeholder.
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

// activationCatalog maps every catalog resource to its activation mode. It is
// total: activationFor still falls back to activeInactive defensively, but
// TestActivationCatalogIsTotal asserts there is an explicit entry for every
// authz.AllResourceTypes() member, so a newly added resource cannot ship without
// a deliberate activation decision here.
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
	// #712: control-plane resources use internal/controlplane.Service's
	// Authority-based PEP but intentionally reject custom policy. They are
	// deployment-administered, not user-owned: admin-full-access is the sole
	// grant, and default deny protects every other authority.
	authz.ResourceProvider: activeInactive,
	authz.ResourceSettings: activeInactive,
	authz.ResourcePlugin:   activeInactive,
	authz.ResourceChannel:  activeInactive,

	// The remaining resources are deliberately inactive: protected, but by a
	// dedicated mechanism rather than the custom-policy Authorizer. #712 is the
	// final stack, so none of these will ever be flipped active.

	// Connection, Email, Share, and Recally are Authority-bound user capabilities,
	// not custom-policy resources.
	authz.ResourceConnection: activeInactive,
	authz.ResourceEmail:      activeInactive,
	authz.ResourceShare:      activeInactive,
	authz.ResourceRecally:    activeInactive,
	// User-owned inbox/memory/notify data, authorized by session/user ownership
	// at the transport; there is no custom-policy PEP over it.
	authz.ResourceUserData: activeInactive,
	// Public tool capability: the invocation is the protected thing, there is no
	// owned resource to attach a policy to.
	authz.ResourceTool: activeInactive,
	// User administration, enforced by the requireAdmin / requireUserTarget gates.
	authz.ResourceUser: activeInactive,
	// Group administration, enforced by the requireGroupOwner gate.
	authz.ResourceGroup: activeInactive,
	// Group membership administration, enforced by the requireGroupOwner gate.
	authz.ResourceMembership: activeInactive,
	// Personal access tokens, enforced by the credential service.
	authz.ResourceToken: activeInactive,
	// MCP token/service, enforced by its own token auth.
	authz.ResourceMCP: activeInactive,
	// Login/session/provider, enforced by the auth service.
	authz.ResourceAuth: activeInactive,
	// Inbound webhook ingress, PAT self-authenticated at the edge.
	authz.ResourceWebhook: activeInactive,
	// System embedding worker, authorized by River queue-insert authority; there
	// is no per-request auth.
	authz.ResourceEmbeddingJob: activeInactive,
	// Authenticated read-only reference data (models/status/builtins).
	authz.ResourceSystemCatalog: activeInactive,
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
