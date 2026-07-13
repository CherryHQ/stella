package policy

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestShadowCompareAgreementAndMismatch(t *testing.T) {
	ctx := context.Background()
	az := New(dbtest.New(t))
	user := userAuthority(t, "u1", false)
	req := mustAgentRead(t, "a1", "", "system", false) // new authorizer allows this

	// Legacy allows too -> agreement.
	if res := az.ShadowCompare(ctx, user, req, true); !res.Match || !res.NewAllowed {
		t.Fatalf("expected agreement, got %+v", res)
	}

	// Legacy denies -> mismatch with a populated diagnostic.
	res := az.ShadowCompare(ctx, user, req, false)
	if res.Match {
		t.Fatal("expected mismatch when legacy denies but new allows")
	}
	if res.Diagnostic == "" {
		t.Fatal("mismatch must carry a structured diagnostic")
	}
	if res.Revision != 0 {
		t.Fatalf("shadow revision = %d, want 0", res.Revision)
	}
}

// Agent listing parity: any authenticated user may enumerate agents (legacy
// agent_list read), so the new List built-in must agree with a legacy allow.
func TestShadowCompareAgentListParity(t *testing.T) {
	ctx := context.Background()
	az := New(dbtest.New(t))
	user := userAuthority(t, "u1", false)
	req, err := AgentListRequest()
	if err != nil {
		t.Fatalf("agent list request: %v", err)
	}

	// New authorizer allows the list; legacy allows it too -> agreement.
	if res := az.ShadowCompare(ctx, user, req, true); !res.Match || !res.NewAllowed {
		t.Fatalf("agent list should be allowed and agree with legacy, got %+v", res)
	}
}

func TestShadowCompareFailsClosedWhenUnavailable(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	az := New(pool)
	pool.Close()

	user := userAuthority(t, "u1", false)
	req := mustAgentRead(t, "a1", "", "system", false)

	// New authorizer is unavailable -> treated as deny. Agreement with a legacy
	// deny; mismatch (flagged) against a legacy allow.
	if res := az.ShadowCompare(ctx, user, req, false); !res.Match || res.NewAllowed {
		t.Fatalf("unavailable+legacy-deny should agree on deny, got %+v", res)
	}
	res := az.ShadowCompare(ctx, user, req, true)
	if res.Match || res.Diagnostic == "" {
		t.Fatalf("unavailable+legacy-allow should be a flagged mismatch, got %+v", res)
	}
}
