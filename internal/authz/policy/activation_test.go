package policy

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

// #709 made Agent, Session, and Workspace authoritative; #710 added the execution
// domains; #711 makes Vault policy-backed; #712 adds the deployment control-plane
// resources (Provider/Settings/Plugin/Channel). Email, Connection, Share, and
// Recally are Authority-bound user capabilities, not custom-policy resources, so
// they stay inactive along with the system catalog and public tool.
func TestOnlyPolicyBackedResourcesAreActivated(t *testing.T) {
	active := map[authz.ResourceType]bool{
		authz.ResourceAgent:     true,
		authz.ResourceSession:   true,
		authz.ResourceWorkspace: true,
		authz.ResourceWorkflow:  true,
		authz.ResourceScheduler: true,
		authz.ResourceGoal:      true,
		authz.ResourceSkill:     true,
		authz.ResourceVault:     true,
		authz.ResourceProvider:  true,
		authz.ResourceSettings:  true,
		authz.ResourcePlugin:    true,
		authz.ResourceChannel:   true,
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
		authz.ResourceSystemCatalog, authz.ResourceTool, authz.ResourceUser, authz.ResourceMCP,
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
