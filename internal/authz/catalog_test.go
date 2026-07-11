package authz_test

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// TestCatalogVersion pins the catalog schema version so a breaking catalog
// change is a conscious edit.
func TestCatalogVersion(t *testing.T) {
	if authz.CatalogVersion != 1 {
		t.Fatalf("CatalogVersion = %d; a breaking catalog change must be intentional", authz.CatalogVersion)
	}
}

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

// TestResourceTypeCatalog does the same for resource types.
func TestResourceTypeCatalog(t *testing.T) {
	all := authz.AllResourceTypes()
	if len(all) == 0 {
		t.Fatal("empty resource catalog")
	}
	seen := map[string]bool{}
	for _, r := range all {
		if !r.Valid() {
			t.Errorf("catalog resource %v reports invalid", r)
		}
		s := r.String()
		if s == "" || s == "invalid" {
			t.Errorf("resource %v has no name", r)
		}
		if seen[s] {
			t.Errorf("duplicate resource name %q", s)
		}
		seen[s] = true
	}
	if authz.ResourceInvalid.Valid() {
		t.Error("ResourceInvalid must be invalid")
	}
	if authz.ResourceType(250).Valid() {
		t.Error("out-of-range resource must be invalid")
	}
}

// TestVisibilityCatalog covers the decision-visibility catalog.
func TestVisibilityCatalog(t *testing.T) {
	all := authz.AllVisibilities()
	if len(all) != 2 {
		t.Fatalf("visibility catalog size = %d, want 2", len(all))
	}
	for _, v := range all {
		if !v.Valid() {
			t.Errorf("catalog visibility %v reports invalid", v)
		}
	}
	if authz.VisibilityInvalid.Valid() {
		t.Error("VisibilityInvalid must be invalid")
	}
	if authz.Visibility(9).Valid() {
		t.Error("out-of-range visibility must be invalid")
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

// TestGrantKindCatalog covers the grant-kind catalog.
func TestGrantKindCatalog(t *testing.T) {
	all := authz.AllGrantKinds()
	if len(all) == 0 {
		t.Fatal("empty grant-kind catalog")
	}
	seen := map[string]bool{}
	for _, k := range all {
		if !k.Valid() {
			t.Errorf("catalog grant kind %v reports invalid", k)
		}
		if seen[k.String()] {
			t.Errorf("duplicate grant-kind name %q", k.String())
		}
		seen[k.String()] = true
	}
	if authz.GrantInvalid.Valid() {
		t.Error("GrantInvalid must be invalid")
	}
	if authz.GrantKind(200).Valid() {
		t.Error("out-of-range grant kind must be invalid")
	}
}
