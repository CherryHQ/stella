package channel

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

func noopStream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, nil
}

// fastSnapshot returns a snapshot whose fast tier resolves to a usable model.
func fastSnapshot(agentID string) *config.Snapshot {
	return &config.Snapshot{AgentID: agentID, Provider: "openai", ModelFast: "openai/gpt-4o-mini"}
}

func newTestArbiter(load SnapshotLoader, complete CompleteFunc) *LLMSemanticGroupArbiter {
	return &LLMSemanticGroupArbiter{
		loadSnapshot: load,
		buildStream: func(context.Context, string, config.ProviderCreds) (providers.StreamFunc, error) {
			return noopStream, nil
		},
		complete: complete,
		timeout:  semanticTimeout,
		log:      slog.Default(),
	}
}

func completeWith(response string) CompleteFunc {
	return func(context.Context, ai.Model, ai.Context, ai.CompleteOptions, providers.StreamFunc) (ai.AssistantMessage, error) {
		return ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: response}}}, nil
	}
}

func systemMember(id string) SemanticGroupMember {
	return SemanticGroupMember{AgentID: id, Scope: config.AgentScopeSystem, ReplyChannelID: "ch-" + id}
}

func TestParseSemanticDecision(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantErr  bool
		wantSay  bool
		wantAgts []string
	}{
		{name: "plain", raw: `{"should_reply":true,"agents":["a"],"reason":"x"}`, wantSay: true, wantAgts: []string{"a"}},
		{name: "fenced", raw: "```json\n{\"should_reply\":false,\"agents\":[]}\n```", wantSay: false},
		{name: "empty", raw: "  ", wantErr: true},
		{name: "garbage", raw: "not json", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSemanticDecision(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ShouldReply != tc.wantSay {
				t.Errorf("ShouldReply=%v want %v", got.ShouldReply, tc.wantSay)
			}
		})
	}
}

func TestSanitizeSemanticDecision(t *testing.T) {
	req := SemanticGroupRequest{
		Members:       []SemanticGroupMember{systemMember("a"), systemMember("b")},
		MaxResponders: 1,
	}

	t.Run("drops non-members and dupes, applies cap", func(t *testing.T) {
		d := SemanticGroupDecision{ShouldReply: true, RespondingAgents: []string{"a", "a", "ghost", "b"}}
		got := sanitizeSemanticDecision(d, req)
		if len(got.RespondingAgents) != 1 || got.RespondingAgents[0] != "a" {
			t.Fatalf("want [a] after dedup+cap, got %v", got.RespondingAgents)
		}
	})

	t.Run("should_reply false collapses to silence", func(t *testing.T) {
		got := sanitizeSemanticDecision(SemanticGroupDecision{ShouldReply: false, RespondingAgents: []string{"a"}}, req)
		if got.ShouldReply || len(got.RespondingAgents) != 0 {
			t.Fatalf("want silence, got %+v", got)
		}
	})

	t.Run("reply with only invalid ids collapses to silence", func(t *testing.T) {
		got := sanitizeSemanticDecision(SemanticGroupDecision{ShouldReply: true, RespondingAgents: []string{"ghost"}}, req)
		if got.ShouldReply || len(got.RespondingAgents) != 0 {
			t.Fatalf("want silence, got %+v", got)
		}
	})
}

func TestSemanticDecideTargeted(t *testing.T) {
	arb := newTestArbiter(
		func(_ context.Context, id string) (*config.Snapshot, error) { return fastSnapshot(id), nil },
		completeWith(`{"should_reply":true,"agents":["b"],"reason":"asks b"}`),
	)
	got := arb.Decide(context.Background(), SemanticGroupRequest{
		Message:       "how do I reset my password",
		Members:       []SemanticGroupMember{systemMember("a"), systemMember("b")},
		MaxResponders: 3,
	})
	if !got.ShouldReply || len(got.RespondingAgents) != 1 || got.RespondingAgents[0] != "b" {
		t.Fatalf("want single agent b, got %+v", got)
	}
}

func TestSemanticDecideBroadcastCapped(t *testing.T) {
	arb := newTestArbiter(
		func(_ context.Context, id string) (*config.Snapshot, error) { return fastSnapshot(id), nil },
		completeWith(`{"should_reply":true,"agents":["a","b","c"],"reason":"broadcast"}`),
	)
	got := arb.Decide(context.Background(), SemanticGroupRequest{
		Message:       "大家都冒个泡",
		Members:       []SemanticGroupMember{systemMember("a"), systemMember("b"), systemMember("c")},
		MaxResponders: 2,
	})
	if len(got.RespondingAgents) != 2 {
		t.Fatalf("want cap=2, got %v", got.RespondingAgents)
	}
}

func TestSemanticDecideFallbacks(t *testing.T) {
	members := []SemanticGroupMember{systemMember("a")}
	load := func(_ context.Context, id string) (*config.Snapshot, error) { return fastSnapshot(id), nil }

	t.Run("completion error → silence", func(t *testing.T) {
		arb := newTestArbiter(load, func(context.Context, ai.Model, ai.Context, ai.CompleteOptions, providers.StreamFunc) (ai.AssistantMessage, error) {
			return ai.AssistantMessage{}, errors.New("boom")
		})
		if got := arb.Decide(context.Background(), SemanticGroupRequest{Message: "hi", Members: members, MaxResponders: 1}); got.ShouldReply {
			t.Fatalf("want silence on error, got %+v", got)
		}
	})

	t.Run("invalid json → silence", func(t *testing.T) {
		arb := newTestArbiter(load, completeWith("not json"))
		if got := arb.Decide(context.Background(), SemanticGroupRequest{Message: "hi", Members: members, MaxResponders: 1}); got.ShouldReply {
			t.Fatalf("want silence on bad json, got %+v", got)
		}
	})

	t.Run("timeout → silence", func(t *testing.T) {
		arb := newTestArbiter(load, func(ctx context.Context, _ ai.Model, _ ai.Context, _ ai.CompleteOptions, _ providers.StreamFunc) (ai.AssistantMessage, error) {
			<-ctx.Done()
			return ai.AssistantMessage{}, ctx.Err()
		})
		arb.timeout = 20 * time.Millisecond
		if got := arb.Decide(context.Background(), SemanticGroupRequest{Message: "hi", Members: members, MaxResponders: 1}); got.ShouldReply {
			t.Fatalf("want silence on timeout, got %+v", got)
		}
	})

	t.Run("empty message → silence without calling model", func(t *testing.T) {
		called := false
		arb := newTestArbiter(load, func(context.Context, ai.Model, ai.Context, ai.CompleteOptions, providers.StreamFunc) (ai.AssistantMessage, error) {
			called = true
			return ai.AssistantMessage{}, nil
		})
		got := arb.Decide(context.Background(), SemanticGroupRequest{Message: "  ", Members: members, MaxResponders: 1})
		if got.ShouldReply || called {
			t.Fatalf("want silence without model call, got %+v called=%v", got, called)
		}
	})
}

// TestSemanticRoutingIsolation proves a private (restricted-scope) platform
// agent's model is never used to route a shared group decision.
func TestSemanticRoutingIsolation(t *testing.T) {
	loaded := make(map[string]bool)
	load := func(_ context.Context, id string) (*config.Snapshot, error) {
		loaded[id] = true
		return fastSnapshot(id), nil
	}

	t.Run("platform group with only restricted members stays silent", func(t *testing.T) {
		arb := newTestArbiter(load, completeWith(`{"should_reply":true,"agents":["a"]}`))
		members := []SemanticGroupMember{
			{AgentID: "a", Scope: config.AgentScopeRestricted, CreatorID: "u1", ReplyChannelID: "ch-a"},
		}
		got := arb.Decide(context.Background(), SemanticGroupRequest{Message: "hi", Members: members, MaxResponders: 1})
		if got.ShouldReply {
			t.Fatalf("restricted-only platform group must stay silent, got %+v", got)
		}
		if loaded["a"] {
			t.Fatalf("must not load snapshot for ineligible restricted agent")
		}
	})

	t.Run("web owner restricted agent is eligible for its own group", func(t *testing.T) {
		arb := newTestArbiter(load, completeWith(`{"should_reply":true,"agents":["a"]}`))
		members := []SemanticGroupMember{
			{AgentID: "a", Scope: config.AgentScopeRestricted, CreatorID: "owner-1", ReplyChannelID: "ch-a"},
		}
		got := arb.Decide(context.Background(), SemanticGroupRequest{
			Message:       "hi",
			Members:       members,
			OwnerUserID:   "owner-1",
			MaxResponders: 1,
		})
		if !got.ShouldReply || len(got.RespondingAgents) != 1 {
			t.Fatalf("web owner's own agent should route, got %+v", got)
		}
	})

	t.Run("web group prefers owner agent over system member", func(t *testing.T) {
		var used string
		loadTrack := func(_ context.Context, id string) (*config.Snapshot, error) {
			if used == "" {
				used = id
			}
			return fastSnapshot(id), nil
		}
		arb := newTestArbiter(loadTrack, completeWith(`{"should_reply":false,"agents":[]}`))
		members := []SemanticGroupMember{
			systemMember("sys"),
			{AgentID: "own", Scope: config.AgentScopeRestricted, CreatorID: "owner-1", ReplyChannelID: "ch-own"},
		}
		arb.Decide(context.Background(), SemanticGroupRequest{
			Message:       "hi",
			Members:       members,
			OwnerUserID:   "owner-1",
			MaxResponders: 1,
		})
		if used != "own" {
			t.Fatalf("owner-matched agent should be tried first, used %q", used)
		}
	})
}
