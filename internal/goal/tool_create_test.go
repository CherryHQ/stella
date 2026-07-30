package goal

import (
	"context"
	"testing"
)

func TestGoalToolCreateHonorsKindAndActivate(t *testing.T) {
	h := newHarness(t)
	handler := goalHandler{
		svc:       h.bundle,
		authority: h.userAuth(t, h.userID),
		agentID:   h.agentID,
	}
	activate := true

	output, err := handler.Create(context.Background(), ToolCreateInput{
		Title:    "direct tool leaf",
		Kind:     KindLeaf,
		Activate: &activate,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, ok := output.(goalResponse)
	if !ok {
		t.Fatalf("goal tool create returned %T, want goalResponse", output)
	}
	if created.Kind != KindLeaf || created.Lifecycle != LifecyclePending {
		t.Fatalf("created goal = %s/%s, want leaf/pending", created.Kind, created.Lifecycle)
	}
}
