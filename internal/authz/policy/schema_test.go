package policy

import (
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// Control-plane resources intentionally have no custom-policy schema: their
// Authorizer PEP uses only resource/action/id and built-in admin access.
func TestPolicyResourcesHaveSchemas(t *testing.T) {
	for _, rt := range authz.AllResourceTypes() {
		if rt == authz.ResourceProvider || rt == authz.ResourceSettings || rt == authz.ResourcePlugin || rt == authz.ResourceChannel {
			if _, ok := schemaFor(rt); ok {
				t.Errorf("control-plane resource %s must not have custom-policy facts", rt)
			}
			continue
		}
		if _, ok := schemaFor(rt); !ok {
			t.Errorf("resource %s has no custom-policy attribute schema", rt)
		}
		if _, err := NewResourceBuilder(rt, "id", "owner").Build(); err != nil {
			t.Errorf("resource %s: builder rejected bare resource: %v", rt, err)
		}
	}
}

func TestAgentResourceBuilderValidatesAttributes(t *testing.T) {
	// Valid scope + assigned.
	if _, err := AgentResource("a1", "u1", AgentFacts{Scope: "system", Assigned: true, Creator: "u1", Status: "enabled"}); err != nil {
		t.Fatalf("valid agent resource rejected: %v", err)
	}
	// Unknown enum value fails.
	if _, err := AgentResource("a1", "u1", AgentFacts{Scope: "galaxy", Assigned: true, Creator: "u1", Status: "enabled"}); !errors.Is(err, ErrSchema) {
		t.Fatalf("bad scope enum: got %v, want ErrSchema", err)
	}
	// Unknown attribute fails.
	_, err := NewResourceBuilder(authz.ResourceAgent, "a1", "u1").WithString("nope", "x").Build()
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("unknown attribute: got %v, want ErrSchema", err)
	}
	// Wrong kind fails (assigned is bool, not string).
	_, err = NewResourceBuilder(authz.ResourceAgent, "a1", "u1").WithString("assigned", "true").Build()
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("wrong kind: got %v, want ErrSchema", err)
	}
}

func TestResourceAttributesAreImmutable(t *testing.T) {
	res, err := AgentResource("a1", "u1", AgentFacts{Scope: "system", Assigned: true, Creator: "u1", Status: "enabled"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := res.Attrs()
	got["scope"] = "mutated"
	if v, _ := res.Attr("scope"); v != "system" {
		t.Fatalf("mutating the returned attrs map changed the resource: %q", v)
	}
}

func TestValidatePredicatesRejectsUnknowns(t *testing.T) {
	cases := []struct {
		name  string
		rt    authz.ResourceType
		preds []predicate
	}{
		{"unknown attr", authz.ResourceAgent, []predicate{{Attr: "ghost", Op: opEq, Value: "x"}}},
		{"bad enum value", authz.ResourceAgent, []predicate{{Attr: "scope", Op: opEq, Value: "nope"}}},
		{"bad bool value", authz.ResourceAgent, []predicate{{Attr: "assigned", Op: opEq, Value: "yes"}}},
		{"in without values", authz.ResourceAgent, []predicate{{Attr: "scope", Op: opIn}}},
		{"disallowed op on bool", authz.ResourceAgent, []predicate{{Attr: "assigned", Op: opIn, Values: []string{"true"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePredicates(tc.rt, tc.preds); !errors.Is(err, ErrSchema) {
				t.Fatalf("got %v, want ErrSchema", err)
			}
		})
	}
	// A valid predicate passes.
	if err := validatePredicates(authz.ResourceAgent, []predicate{{Attr: "scope", Op: opEq, Value: "system"}}); err != nil {
		t.Fatalf("valid predicate rejected: %v", err)
	}
}
