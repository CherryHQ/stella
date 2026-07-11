package policy

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

// In this shadow subphase exactly one resource (Agent) is activated; every other
// catalog resource — including the system catalog and public tool entries — is
// inactive and rejects custom-policy writes. This freezes the shadow-only scope.
func TestOnlyAgentIsActivated(t *testing.T) {
	for _, rt := range authz.AllResourceTypes() {
		wantShadow := rt == authz.ResourceAgent
		if got := resourceAcceptsCustomPolicy(rt); got != wantShadow {
			t.Errorf("resource %s: accepts custom policy = %v, want %v", rt, got, wantShadow)
		}
	}
	// System catalog and public tool are explicitly inactive (documented owners).
	for _, rt := range []authz.ResourceType{authz.ResourceSystemCatalog, authz.ResourceTool} {
		if resourceAcceptsCustomPolicy(rt) {
			t.Errorf("resource %s must be inactive in this subphase", rt)
		}
	}
}

func TestInactiveResourceWritesAreRejected(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	svc := NewService(New(pool))

	for _, rt := range []authz.ResourceType{
		authz.ResourceSystemCatalog, authz.ResourceTool, authz.ResourceVault, authz.ResourceGoal,
	} {
		_, _, err := svc.CreatePolicy(ctx, PolicyInput{
			Resource: rt, Action: authz.ActionRead, Effect: EffectAllow,
		})
		if err == nil {
			t.Errorf("write to inactive resource %s should be rejected", rt)
		}
	}
	if got := currentRevision(t, pool); got != 0 {
		t.Fatalf("rejected writes bumped revision to %d, want 0", got)
	}
}
