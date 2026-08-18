package channel

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func claimContext(groupID, agentID string) context.Context {
	return authz.WithAgentID(authz.WithGroupID(context.Background(), groupID), agentID)
}

func TestGroupClaimAtomicSingleWinner(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	tools := NewGroupClaimTools(fx.db).Tools()
	claim := tools[0]
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for _, agentID := range []string{"agent-1", "agent-2"} {
		wg.Go(func() {
			out, err := claim.Execute(claimContext(fx.groupID, agentID), map[string]any{"key": "report"})
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			var value struct {
				OK bool `json:"ok"`
			}
			if err := json.Unmarshal([]byte(out), &value); err != nil {
				t.Errorf("decode: %v", err)
				return
			}
			results <- value.OK
		})
	}
	wg.Wait()
	close(results)
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d, want 1", winners)
	}
}

func TestGroupClaimTTLClamped(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	claim := NewGroupClaimTools(fx.db).Tools()[0]
	ctx := claimContext(fx.groupID, "agent-1")
	if _, err := claim.Execute(ctx, map[string]any{"key": "report", "ttl_seconds": 1}); err != nil {
		t.Fatal(err)
	}
	first, err := fx.q.GetLiveGroupClaim(context.Background(), sqlc.GetLiveGroupClaimParams{GroupID: fx.groupID, Key: "report"})
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseUntil.Sub(time.Now().UTC()) < minimumGroupClaimTTL-time.Second {
		t.Fatalf("ttl was not clamped: %s", first.LeaseUntil)
	}
	if _, err := claim.Execute(ctx, map[string]any{"key": "long-report", "ttl_seconds": float64((48 * time.Hour).Seconds())}); err != nil {
		t.Fatal(err)
	}
	second, err := fx.q.GetLiveGroupClaim(context.Background(), sqlc.GetLiveGroupClaimParams{GroupID: fx.groupID, Key: "long-report"})
	if err != nil {
		t.Fatal(err)
	}
	if second.LeaseUntil.Sub(time.Now().UTC()) > maximumGroupClaimTTL+time.Second {
		t.Fatalf("maximum ttl was not clamped: %s", second.LeaseUntil)
	}
}

func TestOwnerReclaimRefreshesLease(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	claim := NewGroupClaimTools(fx.db).Tools()[0]
	ctx := claimContext(fx.groupID, "agent-1")
	if _, err := claim.Execute(ctx, map[string]any{"key": "report", "ttl_seconds": 60.0}); err != nil {
		t.Fatal(err)
	}
	first, err := fx.q.GetLiveGroupClaim(context.Background(), sqlc.GetLiveGroupClaimParams{GroupID: fx.groupID, Key: "report"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claim.Execute(ctx, map[string]any{"key": "report", "ttl_seconds": float64((2 * time.Hour).Seconds())}); err != nil {
		t.Fatal(err)
	}
	second, err := fx.q.GetLiveGroupClaim(context.Background(), sqlc.GetLiveGroupClaimParams{GroupID: fx.groupID, Key: "report"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.LeaseUntil.After(first.LeaseUntil) || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("owner refresh=%+v first=%+v", second, first)
	}
}

func TestGroupClaimNonOwnerReleaseRejected(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	tools := NewGroupClaimTools(fx.db).Tools()
	if _, err := tools[0].Execute(claimContext(fx.groupID, "agent-1"), map[string]any{"key": "report"}); err != nil {
		t.Fatal(err)
	}
	out, err := tools[1].Execute(claimContext(fx.groupID, "agent-2"), map[string]any{"key": "report"})
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"ok":false}` {
		t.Fatalf("non-owner release=%s", out)
	}
	if _, err := fx.q.GetLiveGroupClaim(context.Background(), sqlc.GetLiveGroupClaimParams{GroupID: fx.groupID, Key: "report"}); err != nil {
		t.Fatal("claim disappeared")
	}
}

func TestGroupClaimSimultaneousExpiryTakeoverSingleWinner(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	claim := NewGroupClaimTools(fx.db).Tools()[0]
	if _, err := claim.Execute(claimContext(fx.groupID, "agent-1"), map[string]any{"key": "report"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_claim SET lease_until = now() - interval '1 second' WHERE group_id=$1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	winners := make(chan bool, 2)
	for _, id := range []string{"agent-1", "agent-2"} {
		wg.Add(1)
		go func(agentID string) {
			defer wg.Done()
			out, err := claim.Execute(claimContext(fx.groupID, agentID), map[string]any{"key": "report"})
			if err != nil {
				t.Errorf("takeover: %v", err)
				return
			}
			var r struct {
				OK bool `json:"ok"`
			}
			_ = json.Unmarshal([]byte(out), &r)
			winners <- r.OK
		}(id)
	}
	wg.Wait()
	close(winners)
	count := 0
	for ok := range winners {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("takeover winners=%d", count)
	}
}

func TestPromptForbidsChatTurnClaims(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	claim := NewGroupClaimTools(fx.db).Tools()[0]
	_, err := claim.Execute(claimContext(fx.groupID, "agent-1"), map[string]any{"key": "reply"})
	if err == nil || !strings.Contains(err.Error(), "never an ordinary chat reply") {
		t.Fatalf("chat-turn claim error=%v", err)
	}
}

func TestExpiredClaimHiddenFromTriageAndPrompt(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	claim := NewGroupClaimTools(fx.db).Tools()[0]
	if _, err := claim.Execute(claimContext(fx.groupID, "agent-1"), map[string]any{"key": "report", "note": "write it"}); err != nil {
		t.Fatal(err)
	}
	if err := fx.db.QueryRow(context.Background(), `UPDATE ctx_group_claim SET lease_until=now()-interval '1 second' WHERE group_id=$1 RETURNING id`, fx.groupID).Scan(new(string)); err != nil {
		t.Fatal(err)
	}
	if got := fx.d.triageClaims(context.Background(), fx.groupID, "agent-2"); len(got) != 0 {
		t.Fatalf("expired triage claims=%v", got)
	}
	if got := NewGroupClaimPromptLoader(fx.db)(context.Background(), fx.groupID, "agent-2"); len(got) != 0 {
		t.Fatalf("expired prompt claims=%v", got)
	}
}
