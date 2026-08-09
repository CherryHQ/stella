package agentctx

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestEnterSessionCallRejectsDepthAtBoundary(t *testing.T) {
	ctx := context.Background()
	for depth := 1; depth <= MaxCallDepth; depth++ {
		var err error
		ctx, err = EnterSessionCall(ctx, "session-root", "session-"+string(rune('a'+depth)))
		if err != nil {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
	if _, err := EnterSessionCall(ctx, "ignored", "one-too-deep"); err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("depth %d error = %v, want depth limit", MaxCallDepth+1, err)
	}
}

func TestEnterSessionCallRejectsAncestorCycle(t *testing.T) {
	ctx, err := EnterSessionCall(context.Background(), "session-a", "session-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnterSessionCall(ctx, "session-b", "session-a"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("A -> B -> A error = %v, want cycle", err)
	}
}

func TestBindSessionCallTargetForCreate(t *testing.T) {
	ctx, err := EnterSessionCall(context.Background(), "session-a", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = BindSessionCallTarget(ctx, "session-b")
	if err != nil {
		t.Fatal(err)
	}
	call, ok := SessionCallFromContext(ctx)
	if !ok || call.Depth != 1 || !slices.Equal(call.Ancestry, []string{"session-a", "session-b"}) {
		t.Fatalf("call = %+v, present=%v", call, ok)
	}

	call.Ancestry[0] = "mutated"
	again, _ := SessionCallFromContext(ctx)
	if again.Ancestry[0] != "session-a" {
		t.Fatal("SessionCallFromContext exposed mutable ancestry")
	}
}
