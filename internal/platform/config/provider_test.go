package config

import "testing"

func TestProviderIndex_ResolvesByCanonicalID(t *testing.T) {
	ix := NewProviderIndex([]Provider{{ID: "prov-1", Type: "openai"}, {ID: "prov-2", Type: "openai"}})
	p, ok := ix.Lookup("prov-2")
	if !ok || p.ID != "prov-2" {
		t.Errorf("Lookup(prov-2) = %+v, %v", p, ok)
	}
}

// The type alias is a convenience for the common single-provider deployment.
func TestProviderIndex_ResolvesUniqueTypeAlias(t *testing.T) {
	ix := NewProviderIndex([]Provider{{ID: "prov-1", Type: "openai", APIKey: "sk-1"}})
	p, ok := ix.Lookup("openai")
	if !ok || p.ID != "prov-1" {
		t.Errorf("Lookup(openai) = %+v, %v; want the single openai row", p, ok)
	}
}

// Two providers of a type make the bare type name ambiguous, and picking one
// would quietly bill the wrong account. The alias has to disappear.
func TestProviderIndex_AmbiguousTypeAliasDoesNotResolve(t *testing.T) {
	ix := NewProviderIndex([]Provider{{ID: "prov-1", Type: "openai"}, {ID: "prov-2", Type: "openai"}})
	if p, ok := ix.Lookup("openai"); ok {
		t.Errorf("Lookup(openai) resolved to %+v, want no match", p)
	}
}

// A row whose ID happens to equal another row's type name keeps its own identity.
func TestProviderIndex_CanonicalIDWinsOverAlias(t *testing.T) {
	ix := NewProviderIndex([]Provider{{ID: "openai", APIKey: "sk-real"}, {ID: "prov-2", Type: "openai", APIKey: "sk-alias"}})
	p, ok := ix.Lookup("openai")
	if !ok || p.APIKey != "sk-real" {
		t.Errorf("Lookup(openai) = %+v, want the row whose ID is openai", p)
	}
}

func TestProviderIndex_EmptyRefNeverResolves(t *testing.T) {
	ix := NewProviderIndex([]Provider{{ID: "prov-1", Type: "openai"}})
	if p, ok := ix.Lookup(""); ok {
		t.Errorf("Lookup(\"\") resolved to %+v, want no match", p)
	}
}
