package plugin

import (
	"errors"
	"testing"
)

func TestResourceKeyIDRoundTripIsScopedAndCanonical(t *testing.T) {
	key := ResourceKey{Scope: ScopeUserAgent, UserID: "user/one", AgentID: "agent+one", Kind: ResourcePlugin, Name: "demo"}
	id := key.ID()
	if id == "" {
		t.Fatal("valid resource key produced an empty ID")
	}
	got, err := ParseResourceID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got != key {
		t.Fatalf("round trip = %+v, want %+v", got, key)
	}
	if _, err := ParseResourceID(id + "x"); !errors.Is(err, ErrInvalidResourceID) {
		t.Fatalf("tampered ID error = %v", err)
	}
	if (ResourceKey{Scope: ScopeUser, UserID: "", Kind: ResourcePlugin, Name: "demo"}).ID() != "" {
		t.Fatal("invalid owner tuple received an ID")
	}
}
