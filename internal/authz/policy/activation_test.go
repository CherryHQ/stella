package policy

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

// #709 made Agent, Session, and Workspace authoritative; #710 added the execution
// domains; #711 the user-capability domains; #712 the deployment control-plane
// resources (Provider/Settings/Plugin/Channel). Every remaining resource — the
// system catalog, public tool, and the identity/token/mcp/webhook families whose
// enforcement lives in dedicated mechanisms, not the custom-policy Authorizer —
// stays inactive and rejects custom-policy writes.
func TestOnlySessionVerticalIsActivated(t *testing.T) {
	active := map[authz.ResourceType]bool{
		authz.ResourceAgent:      true,
		authz.ResourceSession:    true,
		authz.ResourceWorkspace:  true,
		authz.ResourceWorkflow:   true,
		authz.ResourceScheduler:  true,
		authz.ResourceGoal:       true,
		authz.ResourceSkill:      true,
		authz.ResourceEmail:      true,
		authz.ResourceConnection: true,
		authz.ResourceVault:      true,
		authz.ResourceShare:      true,
		authz.ResourceRecally:    true,
		authz.ResourceProvider:   true,
		authz.ResourceSettings:   true,
		authz.ResourcePlugin:     true,
		authz.ResourceChannel:    true,
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

// TestActivationCatalogIsTotal asserts the catalog carries an explicit entry for
// every catalog resource — never relying on activationFor's defensive default.
// This keeps activation a deliberate, reviewed decision: a newly added
// authz.AllResourceTypes() member cannot ship without an entry here.
func TestActivationCatalogIsTotal(t *testing.T) {
	for _, rt := range authz.AllResourceTypes() {
		if _, ok := activationCatalog[rt]; !ok {
			t.Errorf("resource %s has no explicit activationCatalog entry; every catalog resource needs a deliberate activation decision", rt)
		}
	}
	// The catalog must not carry entries outside the closed catalog either.
	all := make(map[authz.ResourceType]bool, len(authz.AllResourceTypes()))
	for _, rt := range authz.AllResourceTypes() {
		all[rt] = true
	}
	for rt := range activationCatalog {
		if !all[rt] {
			t.Errorf("activationCatalog has entry for %s, which is not in authz.AllResourceTypes()", rt)
		}
	}
}

func TestInactiveResourceWritesAreRejected(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	svc := NewService(New(pool))

	for _, rt := range []authz.ResourceType{
		authz.ResourceSystemCatalog, authz.ResourceTool, authz.ResourceUser, authz.ResourceMCP,
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
