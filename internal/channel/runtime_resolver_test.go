package channel

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
)

type resolverFakeStore struct {
	channel    config.Channel
	channelErr error
	agent      config.Agent
	agentErr   error
}

func (f resolverFakeStore) GetChannel(context.Context, string) (config.Channel, error) {
	return f.channel, f.channelErr
}

func (f resolverFakeStore) GetAgent(context.Context, string) (config.Agent, error) {
	return f.agent, f.agentErr
}

func TestRuntimeResolverChannel(t *testing.T) {
	ctx := context.Background()
	r := NewRuntimeResolver(resolverFakeStore{channel: config.Channel{ID: "telegram-main", Type: "telegram", Enabled: true}})
	ch, err := r.Channel(ctx, "telegram-main")
	if err != nil || ch.ID != "telegram-main" {
		t.Fatalf("Channel = (%+v, %v), want telegram-main", ch, err)
	}

	rErr := NewRuntimeResolver(resolverFakeStore{channelErr: errors.New("boom")})
	if _, err := rErr.Channel(ctx, "telegram-main"); err == nil {
		t.Fatal("Channel error should propagate")
	}
}

func TestRuntimeResolverAgentName(t *testing.T) {
	ctx := context.Background()
	r := NewRuntimeResolver(resolverFakeStore{agent: config.Agent{ID: "a", Name: "Agent A"}})
	if name, ok := r.AgentName(ctx, "a"); !ok || name != "Agent A" {
		t.Fatalf("AgentName = (%q, %v), want (Agent A, true)", name, ok)
	}

	// A missing/unreadable agent is best-effort: empty name, not an error.
	rErr := NewRuntimeResolver(resolverFakeStore{agentErr: errors.New("no agent")})
	if name, ok := rErr.AgentName(ctx, "gone"); ok || name != "" {
		t.Fatalf("AgentName(missing) = (%q, %v), want (\"\", false)", name, ok)
	}
}
