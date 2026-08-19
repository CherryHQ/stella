package channel

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/providers"
)

// An unconfigured fast model is a static fact, not a flaky call: triage must
// fall back to rules silently instead of failing the dispatch three times.
func TestGroupTriageWithoutFastModelUsesRulesWithoutRetrying(t *testing.T) {
	triage := NewLLMGroupTriage(
		func(context.Context, string) (*config.Snapshot, error) {
			return &config.Snapshot{Provider: "demo"}, nil
		},
		func(context.Context, string, config.ProviderCreds) (providers.StreamFunc, error) {
			t.Fatal("no fast model must not reach the provider")
			return nil, nil
		},
	)
	act, reason, err := triage.Decide(context.Background(), GroupTriageRequest{AgentID: "agent-1", GroupID: "group-1"})
	if act || reason != "rules_only" || err != nil {
		t.Fatalf("act=%v reason=%q err=%v, want false/rules_only/nil", act, reason, err)
	}
}
