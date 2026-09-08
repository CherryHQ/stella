package plugin

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDefinitionLifecyclesProjectsRetiredOwnership(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	defs := []Definition{
		{ID: "active", Source: SourceCustom},
		{ID: "held-by-id", Source: SourceCustom, RetiredAt: time.Now()},
		{ID: "held-by-digest", Source: SourceCustom, RetiredAt: time.Now(), Spec: packageSpec(digest)},
		{ID: "waiting", Source: SourceCustom, RetiredAt: time.Now(), Spec: packageSpec("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
	}
	service := &Service{ownerSnapshot: func(context.Context) (ContentOwnerSnapshot, error) {
		return ContentOwnerSnapshot{PluginIDs: []string{"held-by-id"}, Digests: []string{"sha256:" + digest}}, nil
	}}
	got := service.DefinitionLifecycles(context.Background(), defs)

	if got["active"].Status != DefinitionLifecycleInstalled || got["active"].Reason != DefinitionLifecycleActive {
		t.Fatalf("active lifecycle = %#v", got["active"])
	}
	for _, id := range []string{"held-by-id", "held-by-digest"} {
		if got[id].Status != DefinitionLifecycleInUse || got[id].Reason != DefinitionLifecycleRuntimeOwner {
			t.Fatalf("%s lifecycle = %#v, want runtime owner", id, got[id])
		}
	}
	if got["waiting"].Status != DefinitionLifecycleCleanupPending || got["waiting"].Reason != DefinitionLifecycleAwaitingCleanup {
		t.Fatalf("waiting lifecycle = %#v", got["waiting"])
	}
}

func TestDefinitionLifecyclesDoesNotGuessOnOwnerFailure(t *testing.T) {
	def := Definition{ID: "retired", Source: SourceCustom, RetiredAt: time.Now()}
	service := &Service{ownerSnapshot: func(context.Context) (ContentOwnerSnapshot, error) {
		return ContentOwnerSnapshot{}, errors.New("private runtime detail")
	}}
	got := service.DefinitionLifecycles(context.Background(), []Definition{def})[def.ID]
	if got.Status != DefinitionLifecycleCleanupPending || got.Reason != DefinitionLifecycleOwnershipUnconfirmed {
		t.Fatalf("lifecycle = %#v, want ownership uncertainty", got)
	}
}

func packageSpec(digest string) []byte {
	return []byte(`{"origin":"package","content":{"digest":"sha256:` + digest + `"}}`)
}
