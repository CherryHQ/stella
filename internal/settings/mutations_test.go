package settings

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/config"
)

type mutationAgentStore struct{ agent config.Agent }

func (s mutationAgentStore) GetAgent(context.Context, string) (config.Agent, error) {
	return s.agent, nil
}

func (s mutationAgentStore) ListAgents(context.Context) ([]config.Agent, error) {
	return []config.Agent{s.agent}, nil
}

type mutableAssignments struct{ assigned bool }

func (s *mutableAssignments) ListUserAgentIDs(context.Context, string) ([]string, error) {
	if s.assigned {
		return []string{"target"}, nil
	}
	return nil, nil
}

type conditionalOverrideStore struct {
	setCalls   int
	clearCalls int
}

func (*conditionalOverrideStore) Get(context.Context, agent.ToolOverrideKey) (agent.ToolOverride, bool, error) {
	return agent.ToolOverride{}, false, nil
}

func (*conditionalOverrideStore) Set(context.Context, agent.ToolOverrideWrite) error { return nil }
func (*conditionalOverrideStore) Clear(context.Context, agent.ToolOverrideKey) error { return nil }

func (s *conditionalOverrideStore) SetIfDigest(context.Context, agent.ToolOverrideWrite, string) error {
	s.setCalls++
	return nil
}

func (s *conditionalOverrideStore) ClearIfDigest(context.Context, agent.ToolOverrideKey, string) error {
	s.clearCalls++
	return nil
}

type recordingInvalidator struct{ calls int }

func (i *recordingInvalidator) InvalidateUser(string) error              { i.calls++; return nil }
func (i *recordingInvalidator) InvalidateUserAgent(string, string) error { i.calls++; return nil }
func (i *recordingInvalidator) InvalidateAgent(string) error             { i.calls++; return nil }
func (i *recordingInvalidator) InvalidateAll() error                     { i.calls++; return nil }

func TestToolOverrideConfirmReauthorizesAgentScope(t *testing.T) {
	assignments := &mutableAssignments{assigned: true}
	access := agentaccess.NewService(mutationAgentStore{agent: config.Agent{
		ID: "target", Scope: config.AgentScopeRestricted, Enabled: true,
	}}, assignments)
	store := &conditionalOverrideStore{}
	invalidator := &recordingInvalidator{}
	mutator := NewToolOverrideMutator(access, store, invalidator, func(context.Context, string) bool { return true })
	authority := userAuthority(t, "u1")
	request := toolOverrideRequest{ToolName: "custom-tool", Scope: agent.ToolOverrideScopeUserAgent, AgentID: "target", Enabled: true}

	digest, err := mutator.Preview(t.Context(), authority, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	assignments.assigned = false
	if err := mutator.Set(t.Context(), authority, request, digest); !errors.Is(err, agentaccess.ErrForbidden) {
		t.Fatalf("confirm after assignment removal = %v, want forbidden", err)
	}
	if store.setCalls != 0 || invalidator.calls != 0 {
		t.Fatalf("unauthorized confirm reached write/invalidation: set=%d invalidate=%d", store.setCalls, invalidator.calls)
	}
}
