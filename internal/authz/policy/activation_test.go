package policy

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

// #709 made Agent, Session, and Workspace authoritative; #710 added the execution
// domains; #711 makes Vault policy-backed. The other user capabilities (Email,
// Connection, Share, Recally) are enforced by their own domain Access services, not
// custom policy, so they stay inactive — as do the system catalog, public tool, and
// Stack 7 providers/settings/plugins/channels — and reject custom-policy writes.
func TestOnlySessionVerticalIsActivated(t *testing.T) {
	active := map[authz.ResourceType]bool{
		authz.ResourceAgent:     true,
		authz.ResourceSession:   true,
		authz.ResourceWorkspace: true,
		authz.ResourceWorkflow:  true,
		authz.ResourceScheduler: true,
		authz.ResourceGoal:      true,
		authz.ResourceSkill:     true,
		authz.ResourceVault:     true,
	}
	for _, rt := range authz.AllResourceTypes() {
		if got := resourceAcceptsCustomPolicy(rt); got != active[rt] {
			t.Errorf("resource %s: accepts custom policy = %v, want %v", rt, got, active[rt])
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
		authz.ResourceSystemCatalog, authz.ResourceTool, authz.ResourceProvider, authz.ResourceSettings,
		// Email/Connection/Share/Recally are Authority-bound user capabilities, not
		// policy-backed: a custom-policy write for them must fail closed.
		authz.ResourceEmail, authz.ResourceConnection, authz.ResourceShare, authz.ResourceRecally,
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
