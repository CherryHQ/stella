package authz_test

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// TestActionCatalog covers uniqueness, validity of every member, string
// round-trip distinctness, and fail-closed on the zero and out-of-range values.
func TestActionCatalog(t *testing.T) {
	all := authz.AllActions()
	if len(all) == 0 {
		t.Fatal("empty action catalog")
	}
	seen := map[string]bool{}
	for _, a := range all {
		if !a.Valid() {
			t.Errorf("catalog action %v reports invalid", a)
		}
		s := a.String()
		if s == "" || s == "invalid" {
			t.Errorf("action %v has no name", a)
		}
		if seen[s] {
			t.Errorf("duplicate action name %q", s)
		}
		seen[s] = true
	}
	if authz.ActionInvalid.Valid() {
		t.Error("ActionInvalid must be invalid")
	}
	if authz.Action(200).Valid() {
		t.Error("out-of-range action must be invalid")
	}
}

// TestActorKindCatalog covers the actor-kind catalog.
func TestActorKindCatalog(t *testing.T) {
	all := authz.AllActorKinds()
	if len(all) != 5 {
		t.Fatalf("actor-kind catalog size = %d, want 5", len(all))
	}
	seen := map[string]bool{}
	for _, k := range all {
		if !k.Valid() {
			t.Errorf("catalog actor kind %v reports invalid", k)
		}
		if seen[k.String()] {
			t.Errorf("duplicate actor-kind name %q", k.String())
		}
		seen[k.String()] = true
	}
	if authz.ActorInvalid.Valid() {
		t.Error("ActorInvalid must be invalid")
	}
	if authz.ActorKind(200).Valid() {
		t.Error("out-of-range actor kind must be invalid")
	}
}
