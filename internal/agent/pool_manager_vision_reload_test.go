package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

func TestReloadVisionSettingsRebuildsRunnerFactories(t *testing.T) {
	const agentID = "vision-reload"
	pm, store, _ := newSyncLifecyclePool(t, agentID)

	refreshed := make(chan string, 2)
	pm.runnerFuncRefreshedHook = func(_ *Service, snap *config.Snapshot) {
		refreshed <- snap.ModelVision
	}
	for _, want := range []string{"openai/gpt-4o", ""} {
		if err := config.SaveVisionSettings(context.Background(), store, config.VisionSettings{Model: want}); err != nil {
			t.Fatalf("save vision settings %q: %v", want, err)
		}
		if err := pm.ReloadVisionSettings(context.Background()); err != nil {
			t.Fatalf("ReloadVisionSettings: %v", err)
		}
		if got := <-refreshed; got != want {
			t.Fatalf("rebuilt snapshot vision model = %q, want %q", got, want)
		}
	}
}
