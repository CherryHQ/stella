package server

// Catalog-coverage gate for issue #707 (stack 2 subphase A).
//
// TestAuthorizationEntryCoverage (authorization_coverage_test.go) proves every
// live entry is *classified*. This file proves each classification maps onto the
// closed authz catalog: every classified route's action and actor resolves to a
// valid catalog member, and an unknown action/actor fails closed. Together they
// guarantee that a newly added protected entry cannot ship until its action and
// actor exist in internal/authz's catalogs. Resource routing is now owned by each
// domain's own static rules, so there is no central resource catalog to gate.
//
// The Catalog*For helpers are exported so the external server_test entry
// inventory (River workers, builtin tools, channel grants, webhook) can reuse
// the identical mapping — the standard export_test bridge; these symbols exist
// only in the test binary.

import (
	"strings"
	"testing"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/authz"
)

// CatalogActionFor maps a classifier/inventory action verb onto the authz.Action
// catalog. ok=false for an unrecognised verb (fail closed).
func CatalogActionFor(verb string) (authz.Action, bool) {
	switch verb {
	case "read":
		return authz.ActionRead, true
	case "list":
		return authz.ActionList, true
	case "create":
		return authz.ActionCreate, true
	case "write":
		return authz.ActionWrite, true
	case "delete":
		return authz.ActionDelete, true
	case "execute":
		return authz.ActionExecute, true
	case "manage":
		return authz.ActionManage, true
	case "use":
		return authz.ActionUse, true
	default:
		return authz.ActionInvalid, false
	}
}

// CatalogActorKindsFor maps an actor label onto one or more authz.ActorKind. A
// label may be qualified ("SystemActor(webhook-ingress)") or compound
// ("UserActor(channel-DM)/AgentActor"). The public "Anonymous" label maps to no
// kind (ok=true, empty slice): public entries need no Authority. ok=false for an
// unrecognised actor token (fail closed).
func CatalogActorKindsFor(label string) ([]authz.ActorKind, bool) {
	var kinds []authz.ActorKind
	for part := range strings.SplitSeq(label, "/") {
		base := part
		if i := strings.IndexByte(base, '('); i >= 0 {
			base = base[:i]
		}
		switch strings.TrimSpace(base) {
		case "Anonymous":
			// public: no Authority kind.
		case "UserActor":
			kinds = append(kinds, authz.ActorUser)
		case "AgentActor":
			kinds = append(kinds, authz.ActorAgent)
		case "GroupIngressActor":
			// A group-ingress turn is realized as a group-agent actor; there is no
			// separate plain-group actor kind.
			kinds = append(kinds, authz.ActorGroupAgent)
		case "GroupAgentActor":
			kinds = append(kinds, authz.ActorGroupAgent)
		case "SystemActor":
			kinds = append(kinds, authz.ActorSystem)
		default:
			return nil, false
		}
	}
	return kinds, true
}

func TestAuthorizationRouteCatalogMapping(t *testing.T) {
	rm := &recordingMux{}
	apiserver.HandlerFromMux(&Server{}, rm)

	mapped := 0
	for _, p := range rm.patterns {
		method, path, ok := strings.Cut(p, " ")
		if !ok || !strings.HasPrefix(path, "/api/") {
			continue
		}
		class, classified := classifyAPIRoute(method, path)
		if !classified {
			// TestAuthorizationEntryCoverage owns the unclassified failure.
			continue
		}
		mapped++

		if act, ok := CatalogActionFor(class.Action); !ok || !act.Valid() {
			t.Errorf("route %s %s action %q has no valid authz.Action catalog member", method, path, class.Action)
		}
		kinds, ok := CatalogActorKindsFor(class.Actor)
		if !ok {
			t.Errorf("route %s %s actor %q has no valid authz.ActorKind mapping", method, path, class.Actor)
			continue
		}
		for _, k := range kinds {
			if !k.Valid() {
				t.Errorf("route %s %s actor %q mapped to invalid kind %v", method, path, class.Actor, k)
			}
		}
	}
	if mapped == 0 {
		t.Fatal("no classified routes were mapped to the authz catalog")
	}
	t.Logf("mapped %d classified /api routes onto the authz catalog", mapped)
}

func TestAuthorizationCatalogMappingFailsClosed(t *testing.T) {
	if _, ok := CatalogActionFor("frobnicate"); ok {
		t.Error("unknown action must not map to the catalog")
	}
	if _, ok := CatalogActorKindsFor("WizardActor"); ok {
		t.Error("unknown actor must not map to the catalog")
	}
	// A compound label with one unknown token fails whole.
	if _, ok := CatalogActorKindsFor("UserActor/WizardActor"); ok {
		t.Error("compound label with an unknown token must fail closed")
	}
}
