package channel

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
)

func TestChannelPluginGateAppliesListenerCapToGuest(t *testing.T) {
	coord := &Coordinator{
		listenerCap: func(context.Context, string, string) (bool, error) { return false, nil },
	}

	allowed, err := coord.channelPluginAllowed(t.Context(), &ResolvedChat{
		GuestID: "guest-1",
		ChatCtx: ChatContext{Platform: "feishu"},
	})
	if err != nil || allowed {
		t.Fatalf("guest gate = %v, %v; want listener cap denial without owner snapshot", allowed, err)
	}
}

func TestChannelPluginGateAppliesListenerCapToLinkedActor(t *testing.T) {
	var gotPluginID, gotAgentID string
	coord := &Coordinator{
		listenerCap: func(_ context.Context, pluginID, agentID string) (bool, error) {
			gotPluginID, gotAgentID = pluginID, agentID
			return false, nil
		},
	}

	allowed, err := coord.channelPluginAllowed(t.Context(), &ResolvedChat{
		AgentID: "agent-1",
		ChatCtx: ChatContext{Platform: "feishu"},
	})
	if err != nil || allowed {
		t.Fatalf("linked actor gate = %v, %v; want listener cap denial", allowed, err)
	}
	if gotPluginID != config.PluginID(config.PluginKindChannel, "feishu") || gotAgentID != "agent-1" {
		t.Fatalf("listener cap args = %q, %q", gotPluginID, gotAgentID)
	}
}

func TestChannelPluginGatePropagatesListenerCapFailureBeforeDispatch(t *testing.T) {
	capErr := errors.New("listener capability unavailable")
	coord := &Coordinator{
		listenerCap: func(context.Context, string, string) (bool, error) {
			return false, capErr
		},
	}

	allowed, err := coord.channelPluginAllowed(t.Context(), &ResolvedChat{
		AgentID: "agent-1",
		ChatCtx: ChatContext{Platform: "feishu"},
	})
	if allowed || !errors.Is(err, capErr) {
		t.Fatalf("linked actor gate = %v, %v; want listener cap failure", allowed, err)
	}
}
