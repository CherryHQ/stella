package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

func TestReloadModelDefaultsRebuildsRunnerFactories(t *testing.T) {
	const agentID = "defaults-reload"
	pm, store, _ := newSyncLifecyclePool(t, agentID)

	refreshed := make(chan string, 2)
	pm.runnerFuncRefreshedHook = func(_ *Service, snap *config.Snapshot) {
		refreshed <- snap.ModelVision
	}
	for _, want := range []string{"openai/gpt-4o", ""} {
		if err := config.SaveDefaultModels(context.Background(), store, config.DefaultModels{ModelVision: want}); err != nil {
			t.Fatalf("save default models %q: %v", want, err)
		}
		if err := pm.ReloadModelDefaults(context.Background()); err != nil {
			t.Fatalf("ReloadModelDefaults: %v", err)
		}
		if got := <-refreshed; got != want {
			t.Fatalf("rebuilt snapshot vision model = %q, want %q", got, want)
		}
	}
}

// An agent that names no model of its own must pick up a later deployment
// default: that inheritance is the whole point of the defaults surface, and a
// cached runner factory would otherwise pin the agent to "no model".
func TestReloadModelDefaultsPropagatesInheritedTier(t *testing.T) {
	const agentID = "defaults-inherit"
	pm, store, _ := newSyncLifecyclePool(t, agentID)

	// The fixture agent names its own model; clear it so the tier is the empty
	// "inherit" state a freshly created agent is in.
	ag, err := store.GetAgent(context.Background(), agentID)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	ag.Model = ""
	if err := store.UpdateAgent(context.Background(), ag); err != nil {
		t.Fatalf("clear agent model: %v", err)
	}

	refreshed := make(chan string, 1)
	pm.runnerFuncRefreshedHook = func(_ *Service, snap *config.Snapshot) {
		refreshed <- snap.Model
	}
	const want = "anthropic/claude-sonnet-4-6"
	if err = config.SaveDefaultModels(context.Background(), store, config.DefaultModels{Model: want}); err != nil {
		t.Fatalf("save default models: %v", err)
	}
	if err := pm.ReloadModelDefaults(context.Background()); err != nil {
		t.Fatalf("ReloadModelDefaults: %v", err)
	}
	if got := <-refreshed; got != want {
		t.Fatalf("rebuilt snapshot default model = %q, want the inherited deployment default %q", got, want)
	}
}
