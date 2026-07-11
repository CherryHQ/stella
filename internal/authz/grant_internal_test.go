package authz

import "testing"

// TestGrantKindClassIntentional proves every catalog grant kind maps to its
// intended privacy class, and that an unknown/default kind maps to the
// no-actor sentinel classNone rather than a real class.
func TestGrantKindClassIntentional(t *testing.T) {
	want := map[GrantKind]grantClass{
		GrantPublicTool:        classPublic,
		GrantGroupTool:         classGroup,
		GrantAgentTool:         classUserPrivate,
		GrantEntryScope:        classUserPrivate,
		GrantChannelBinding:    classUserPrivate,
		GrantSystemMaintenance: classSystem,
	}

	all := AllGrantKinds()
	if len(want) != len(all) {
		t.Fatalf("class map covers %d kinds, catalog has %d; keep them in lockstep", len(want), len(all))
	}
	for _, k := range all {
		got := k.class()
		if got == classNone {
			t.Errorf("catalog grant kind %s maps to the no-actor sentinel classNone", k)
		}
		if want[k] != got {
			t.Errorf("grant kind %s class = %d, want %d", k, got, want[k])
		}
	}

	// The zero and out-of-range kinds are unclassified and must map to classNone.
	if GrantInvalid.class() != classNone {
		t.Error("GrantInvalid must map to classNone")
	}
	if GrantKind(99).class() != classNone {
		t.Error("out-of-range grant kind must map to classNone")
	}
}

// TestUnknownGrantRejectedByEveryActor proves a grant whose kind is outside the
// catalog (classNone) is rejected by every actor check — no actor variant, valid
// or invalid, may hold it. This is defence in depth behind NewGrant/NewGrantSet,
// which already reject an invalid kind before it can enter a real GrantSet.
func TestUnknownGrantRejectedByEveryActor(t *testing.T) {
	// Fabricate an unknown-kind grant directly (unexported fields, same package)
	// to bypass NewGrant's validation.
	unknown := GrantSet{grants: []Grant{{kind: GrantKind(99), key: "x"}}}

	kinds := append(AllActorKinds(), ActorInvalid)
	for _, k := range kinds {
		if err := checkGrantsForActor(k, unknown); err == nil {
			t.Errorf("actor kind %s accepted an unknown-class grant; every actor must reject classNone", k)
		}
	}

	// Sanity: no actor's allowedClasses contains classNone.
	for _, k := range AllActorKinds() {
		if allowedClasses(k)[classNone] {
			t.Errorf("actor kind %s allows classNone", k)
		}
	}
}
