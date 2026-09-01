package agent

import (
	"context"
	"testing"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/config"
)

func TestPoolManagerOrdinaryTurnUsesNormalModel(t *testing.T) {
	const agentID = "normal-runtime-model"
	pm, store, ag := newSyncLifecyclePool(t, agentID)
	ag.Model = "deepseek/deepseek-chat"
	ag.ModelThinking = "low"
	ag.ModelStrong = "cpa/gpt-5.6-sol"
	ag.ModelStrongThinking = "high"
	if err := store.UpdateAgent(context.Background(), ag); err != nil {
		t.Fatalf("update agent models: %v", err)
	}
	if err := pm.SyncAgent(context.Background(), agentID); err != nil {
		t.Fatalf("reload agent: %v", err)
	}

	params := make(chan agentruntime.RunnerParams, 1)
	svc := pm.GetService(agentID)
	svc.Runtime.SetNewRunner(func(_ context.Context, got agentruntime.RunnerParams) (agentruntime.Runner, error) {
		params <- got
		return &ownerFenceRunner{}, nil
	})
	stream, err := svc.admit(context.Background(), session.Info{
		ID: "normal-turn", UserID: "user", AgentID: agentID,
		Kind: string(session.KindChat), Channel: string(session.ChannelWeb),
	}, "hello")
	if err != nil {
		t.Fatalf("admit turn: %v", err)
	}
	for range stream {
	}
	got := <-params
	if got.Model != ag.Model || got.Thinking != ag.ModelThinking {
		t.Fatalf("ordinary runner model/thinking = %q/%q, want normal %q/%q", got.Model, got.Thinking, ag.Model, ag.ModelThinking)
	}
}

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
