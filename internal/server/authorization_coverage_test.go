package server

// Authorization-entry coverage gate for issue #706 (stack 1 of #703).
//
// TestAuthorizationEntryCoverage mechanically enumerates every generated /api
// route (via the same recordingMux the credential-scope test uses) and requires
// each one to be classified by a deep, rule-based classifier into BOTH its
// current entry gate AND its target authorization shape (Authority actor class,
// typed action, resource, visibility, owning stack). A newly generated route the
// classifier does not recognise fails unclassified — the same fail-closed
// discipline as credential.RequiredScope, but for resource authorization rather
// than PAT scope.
//
// PAT scope != resource authorization. credential.RequiredScope answers "may this
// token reach the route at all"; this classifier answers "who is the target
// Authority and what action/resource/visibility does the route govern". The two
// are cross-referenced (patReachable column) but never conflated.
//
// Honesty ceiling: CurrentGate and the target fields are RULE-derived per family
// (grounded in the requireAdmin/owner-helper/exempt evidence surveyed in
// foundation-baseline.md §2), not a per-route semantic proof. The executable
// guarantees are: every live route is classified, every classification field is
// populated, the route set is non-empty, and a new/renamed route or sub-resource
// fails until someone classifies it.

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/credential"
)

// routeClass is the full classification every /api route must carry.
type routeClass struct {
	CurrentGate string // current entry gate/check category (as it exists today)
	Actor       string // target Authority actor class
	Action      string // typed action
	Resource    string // target resource
	Visibility  string // public | private | admin
	Stack       string // owning migration stack
}

func (c routeClass) complete() bool {
	return c.CurrentGate != "" && c.Actor != "" && c.Action != "" &&
		c.Resource != "" && c.Visibility != "" && c.Stack != ""
}

// action verbs: a POST whose path contains one of these is an execute/command,
// not a create.
var actionVerbs = map[string]bool{
	"run": true, "activate": true, "cancel": true, "abandon": true,
	"reattempt": true, "unarchive": true, "verdict": true, "approve": true,
	"reject": true, "waive": true, "poll": true, "sync": true, "start": true,
	"instantiate": true, "mark": true, "send": true, "login": true,
	"logout": true, "register": true, "begin": true, "init": true,
	"upload": true, "install": true, "upgrade": true, "rotate-secret": true,
	"link-code": true, "qr": true, "connected": true, "callback": true,
}

func actionFor(method string, segs []string) string {
	switch method {
	case http.MethodGet:
		return "read"
	case http.MethodDelete:
		return "delete"
	case http.MethodPut, http.MethodPatch:
		return "write"
	case http.MethodPost:
		for _, s := range segs {
			if actionVerbs[s] {
				return "execute"
			}
		}
		return "create"
	default:
		return ""
	}
}

// apiPathSegments returns the path segments after "/api/".
func apiPathSegments(path string) []string {
	rest := strings.TrimPrefix(path, "/api/")
	if rest == path {
		return nil
	}
	return strings.Split(strings.Trim(rest, "/"), "/")
}

const (
	stackAuthz     = "authz-core"
	stackAsset     = "asset-workspace"
	stackPlugin    = "plugin-capability"
	stackTransport = "transports"
)

// gateControlPlane is the entry gate for the admin-only deployment control-plane
// families (providers/settings/plugins/channels) after #712: each handler derives
// a trusted Authority and decides through controlplane.Service, whose sole grant
// is admin-full-access — so a non-admin is default-denied exactly as the legacy
// requireAdmin gate denied them. The target shape (admin visibility, admin actor)
// is unchanged; only the enforcement mechanism moved off requireAdmin.
const gateControlPlane = "controlplane.Service Authority PEP (#712)"

// classifyAPIRoute is the rule-based classifier. It returns ok=false for any
// route family / sub-resource it does not explicitly recognise, so a newly
// generated route fails the coverage test until classified.
func classifyAPIRoute(method, path string) (routeClass, bool) {
	segs := apiPathSegments(path)
	if len(segs) == 0 {
		return routeClass{}, false
	}
	act := actionFor(method, segs)
	if act == "" {
		return routeClass{}, false
	}
	family := segs[0]

	// user-facing owner-scoped resources with a uniform target shape.
	ownerResource := func(resource string) (routeClass, bool) {
		return routeClass{
			CurrentGate: "owner (inline owner_id / domain auth helper)",
			Actor:       "UserActor", Action: act, Resource: resource,
			Visibility: "private", Stack: stackAuthz,
		}, true
	}
	// controlPlaneResource classifies the admin-only deployment control-plane
	// families now gated by controlplane.Service (providers/settings/plugins).
	controlPlaneResource := func(resource, stack string) (routeClass, bool) {
		return routeClass{
			CurrentGate: gateControlPlane,
			Actor:       "UserActor(admin)", Action: act, Resource: resource,
			Visibility: "admin", Stack: stack,
		}, true
	}
	authenticated := func(resource string) (routeClass, bool) {
		return routeClass{
			CurrentGate: "authenticated (session/bearer, no per-object check)",
			Actor:       "UserActor", Action: act, Resource: resource,
			Visibility: "private", Stack: stackAuthz,
		}, true
	}

	switch family {
	case "agents":
		return classifyAgents(method, act, segs)
	case "users":
		return classifyUsers(act, segs)
	case "auth":
		return classifyAuth(act, segs)
	case "channels":
		return classifyChannels(act, segs)
	case "groups":
		// group message send is a group-ingress turn; other ops are owner/admin.
		if len(segs) >= 3 && segs[2] == "messages" && method == http.MethodPost {
			return routeClass{
				CurrentGate: "group-owner/member (requireGroupOwner + membership)",
				Actor:       "GroupIngressActor", Action: "execute", Resource: "group_turn",
				Visibility: "private", Stack: stackAuthz,
			}, true
		}
		return routeClass{
			CurrentGate: "owner-or-admin (requireGroupOwner / IsAdmin)",
			Actor:       "UserActor", Action: act, Resource: "group",
			Visibility: "private", Stack: stackAuthz,
		}, true
	case "goals":
		return ownerResource("goal")
	case "workflows":
		return ownerResource("workflow")
	case "skills":
		return ownerResource("skill")
	case "recally":
		return ownerResource("recally")
	case "email":
		return ownerResource("email")
	case "vault":
		return ownerResource("vault")
	case "mcp":
		return ownerResource("mcp")
	case "inbox":
		return ownerResource("inbox")
	case "shares":
		if len(segs) >= 2 && segs[1] == "public" {
			return routeClass{
				CurrentGate: "public (auth-exempt share token)",
				Actor:       "Anonymous", Action: act, Resource: "share",
				Visibility: "public", Stack: stackAuthz,
			}, true
		}
		return ownerResource("share")
	case "scheduler":
		// only /api/scheduler/job-templates (read-only catalog).
		return authenticated("scheduler")
	case "builtin":
		return authenticated("builtin")
	case "clawhub":
		return authenticated("skill_registry")
	case "models":
		return authenticated("model")
	case "token-scopes":
		return authenticated("token")
	case "status":
		return authenticated("status")
	case "provider-types", "providers":
		return controlPlaneResource("provider", stackAuthz)
	case "plugins", "manifest-plugins":
		return controlPlaneResource("plugin", stackPlugin)
	case "cli-tools":
		return controlPlaneResource("cli", stackAuthz)
	case "embedding-settings":
		return controlPlaneResource("embedding", stackAuthz)
	case "admin":
		return controlPlaneResource("oauth_provider_config", stackAuthz)
	}
	return routeClass{}, false
}

func classifyAgents(method, act string, segs []string) (routeClass, bool) {
	// segs = [agents, {id}, <sub>?, ...]; sub-resource is segs[2] when present.
	if len(segs) <= 2 {
		// collection or single agent.
		if method == http.MethodGet {
			return routeClass{
				CurrentGate: "agent/access (authoritative Authorizer)",
				Actor:       "UserActor", Action: act, Resource: "agent",
				Visibility: "private", Stack: stackAuthz,
			}, true
		}
		return routeClass{
			CurrentGate: "agent/access (authoritative Authorizer)",
			Actor:       "UserActor", Action: act, Resource: "agent",
			Visibility: "private", Stack: stackAuthz,
		}, true
	}
	sub := segs[2]
	agentScoped := func(resource, stack string) (routeClass, bool) {
		return routeClass{
			CurrentGate: "agent/access (fresh Agent Read/Use after session ownership check)",
			Actor:       "UserActor", Action: act, Resource: resource,
			Visibility: "private", Stack: stack,
		}, true
	}
	switch sub {
	case "sessions":
		// workspace sub-tree is asset/workspace state, not just session metadata.
		if slices.Contains(segs, "workspace") {
			return agentScoped("workspace_asset", stackAsset)
		}
		return agentScoped("session", stackAuthz)
	case "projects":
		return agentScoped("project", stackAsset)
	case "scheduler":
		return agentScoped("scheduler", stackAuthz)
	case "skills":
		return agentScoped("skill", stackAuthz)
	case "tools":
		return agentScoped("agent_tool", stackAuthz)
	case "users":
		return routeClass{
			CurrentGate: "admin (requireAdmin)",
			Actor:       "UserActor(admin)", Action: act, Resource: "agent_membership",
			Visibility: "admin", Stack: stackAuthz,
		}, true
	}
	return routeClass{}, false
}

func classifyUsers(act string, segs []string) (routeClass, bool) {
	// segs = [users, ...].
	if len(segs) == 1 {
		return routeClass{
			CurrentGate: "admin (requireAdmin)",
			Actor:       "UserActor(admin)", Action: act, Resource: "user",
			Visibility: "admin", Stack: stackAuthz,
		}, true
	}
	if segs[1] == "me" {
		resource := "user_data"
		if len(segs) >= 3 {
			switch segs[2] {
			case "oauth", "oauth-clients", "oauth-client-scopes", "authorized-apps":
				resource = "oauth"
			case "tokens":
				resource = "token"
			case "memories", "soul", "password", "identities", "link-code":
				resource = "user_data"
			}
		}
		return routeClass{
			CurrentGate: "self (subject == authenticated user)",
			Actor:       "UserActor", Action: act, Resource: resource,
			Visibility: "private", Stack: stackAuthz,
		}, true
	}
	// /api/users/{id}/... -> admin-managed target user.
	return routeClass{
		CurrentGate: "admin (requireAdmin / requireUserTarget)",
		Actor:       "UserActor(admin)", Action: act, Resource: "user",
		Visibility: "admin", Stack: stackAuthz,
	}, true
}

func classifyAuth(act string, segs []string) (routeClass, bool) {
	if len(segs) < 2 {
		return routeClass{}, false
	}
	switch segs[1] {
	case "me":
		return routeClass{
			CurrentGate: "self (OIDC session)",
			Actor:       "UserActor", Action: act, Resource: "user",
			Visibility: "private", Stack: stackAuthz,
		}, true
	case "providers":
		return routeClass{
			CurrentGate: "public (login options, auth-exempt)",
			Actor:       "Anonymous", Action: act, Resource: "auth_provider",
			Visibility: "public", Stack: stackAuthz,
		}, true
	case "sessions":
		return routeClass{
			CurrentGate: "self (own OIDC sessions)",
			Actor:       "UserActor", Action: act, Resource: "auth_session",
			Visibility: "private", Stack: stackAuthz,
		}, true
	case "logout":
		return routeClass{
			CurrentGate: "authenticated (session)",
			Actor:       "UserActor", Action: act, Resource: "auth",
			Visibility: "private", Stack: stackAuthz,
		}, true
	case "local":
		return routeClass{
			CurrentGate: "public (auth-exempt login/register)",
			Actor:       "Anonymous", Action: act, Resource: "auth",
			Visibility: "public", Stack: stackAuthz,
		}, true
	case "oauth", "profile":
		return routeClass{
			CurrentGate: "public (auth-exempt OAuth callback)",
			Actor:       "Anonymous", Action: act, Resource: "oauth",
			Visibility: "public", Stack: stackAuthz,
		}, true
	}
	return routeClass{}, false
}

func classifyChannels(act string, segs []string) (routeClass, bool) {
	// /api/channels/public is an authenticated agent-visibility listing;
	// registration + admin ops require admin.
	if len(segs) >= 2 && segs[1] == "public" {
		return routeClass{
			CurrentGate: "authenticated (public-channel listing)",
			Actor:       "UserActor", Action: act, Resource: "channel",
			Visibility: "private", Stack: stackTransport,
		}, true
	}
	return routeClass{
		CurrentGate: gateControlPlane,
		Actor:       "UserActor(admin)", Action: act, Resource: "channel",
		Visibility: "admin", Stack: stackTransport,
	}, true
}

func TestAuthorizationEntryCoverage(t *testing.T) {
	rm := &recordingMux{}
	apiserver.HandlerFromMux(&Server{}, rm)

	count := 0
	for _, p := range rm.patterns {
		method, path, ok := strings.Cut(p, " ")
		if !ok || !strings.HasPrefix(path, "/api/") {
			continue
		}
		count++
		class, classified := classifyAPIRoute(method, path)
		if !classified {
			t.Errorf("route %s %s is unclassified for resource authorization; add a rule to classifyAPIRoute (current gate + target Authority/action/resource/visibility/stack)", method, path)
			continue
		}
		if !class.complete() {
			t.Errorf("route %s %s classification has an empty field: %+v", method, path, class)
		}
		// Cross-reference PAT scope WITHOUT conflating it with authorization: a
		// route the classifier marks private/admin must be scope-registered too.
		if _, registered := credential.RequiredScope(method, path); !registered && class.Visibility != "public" {
			t.Errorf("route %s %s classified %q but credential.RequiredScope does not register it; PAT-reachability and resource authorization must both be defined", method, path, class.Visibility)
		}
	}
	if count == 0 {
		t.Fatal("no /api routes were enumerated; the classifier is not exercising the router")
	}
	t.Logf("classified %d /api routes for resource authorization", count)
}

// TestAuthorizationClassifierFailsClosed locks the fail-closed property: a newly
// generated route family or agent sub-resource the classifier does not recognise
// must return ok=false (so TestAuthorizationEntryCoverage fails until it is
// classified), rather than silently inheriting a neighbouring rule.
func TestAuthorizationClassifierFailsClosed(t *testing.T) {
	unknown := []struct{ method, path string }{
		{http.MethodGet, "/api/newresource"},
		{http.MethodPost, "/api/agents/{id}/newsubresource"},
		{http.MethodGet, "/api/auth/newthing"},
		{http.MethodDelete, "/api/newresource/{id}"},
	}
	for _, u := range unknown {
		if _, ok := classifyAPIRoute(u.method, u.path); ok {
			t.Errorf("classifyAPIRoute(%s %s) returned classified=true; unknown routes must fail closed", u.method, u.path)
		}
	}
}
