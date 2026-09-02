package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/platform/config"
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

// A half-configured ref like "demo/" is set but unusable: it resolves to an
// empty model id, so the provider would be asked for no model at all. Agent
// writes reject that shape now, but rows stored before they did still exist and
// must degrade exactly like an unset tier rather than buy a doomed round trip.
func TestFastModelCallerTreatsHalfConfiguredRefAsMissing(t *testing.T) {
	caller := fastModelCaller{
		load: func(context.Context, string) (*config.Snapshot, error) {
			return &config.Snapshot{
				Provider:  "demo",
				ModelFast: "demo/",
				Providers: map[string]config.ProviderCreds{"demo": {Type: "openai", APIKey: "k"}},
			}, nil
		},
		build: func(context.Context, string, config.ProviderCreds) (providers.StreamFunc, error) {
			t.Fatal("an empty model id must not reach the provider")
			return nil, nil
		},
	}
	_, stage, err := caller.Complete(context.Background(), "agent-1", "system", "payload", time.Second)
	if !errors.Is(err, errNoFastModel) || stage != fastModelStageSnapshot {
		t.Fatalf("stage=%q err=%v, want %q/errNoFastModel", stage, err, fastModelStageSnapshot)
	}
}
