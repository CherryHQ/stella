package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/providers"
)

// An unconfigured fast model is a static fact, not a flaky call: every
// classifier sharing this caller must learn that before a provider is built, so
// the caller can fall back deterministically instead of retrying a dead path.
func TestFastModelCallerReportsMissingModelBeforeBuildingProvider(t *testing.T) {
	caller := fastModelCaller{
		load: func(context.Context, string) (*config.Snapshot, error) {
			return &config.Snapshot{Provider: "demo"}, nil
		},
		build: func(context.Context, string, config.ProviderCreds) (providers.StreamFunc, error) {
			t.Fatal("no fast model must not reach the provider")
			return nil, nil
		},
	}
	_, stage, err := caller.Complete(context.Background(), "agent-1", "system", "payload", time.Second)
	if !errors.Is(err, errNoFastModel) || stage != fastModelStageSnapshot {
		t.Fatalf("stage=%q err=%v, want %q/errNoFastModel", stage, err, fastModelStageSnapshot)
	}
}
