package plugins

import (
	"context"
	"strings"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

const (
	ReflectModelTierStrong = "strong"
	ReflectModelTierFast   = "fast"
)

type ReflectProviderCreds struct {
	APIKey  string
	BaseURL string
}

type ReflectAgent struct {
	ID string
}

type ReflectSnapshot struct {
	AgentID      string
	Provider     string
	Model        string
	ModelStrong  string
	ModelFast    string
	Workspace    string
	APIKey       string
	BaseURL      string
	SystemPrompt string
	Providers    map[string]ReflectProviderCreds
}

func (s *ReflectSnapshot) ResolveModelID(tier string) string {
	switch tier {
	case ReflectModelTierFast:
		if s.ModelFast != "" {
			return s.ModelFast
		}
	default:
		if s.ModelStrong != "" {
			return s.ModelStrong
		}
	}
	return s.Model
}

func (s *ReflectSnapshot) ResolveModelTier(tier string) ai.Model {
	modelRef := s.ResolveModelID(tier)
	providerID, modelID := parseReflectModelRef(modelRef)
	if providerID == "" {
		providerID = s.Provider
	}

	baseURL := s.BaseURL
	if creds, ok := s.Providers[providerID]; ok {
		baseURL = creds.BaseURL
	}

	return ai.Model{
		ID:       modelID,
		Name:     modelID,
		API:      providerID,
		Provider: providerID,
		BaseURL:  baseURL,
	}
}

func (s *ReflectSnapshot) ResolveProviderCreds(providerID string) ReflectProviderCreds {
	if providerID == "" {
		providerID = s.Provider
	}
	if creds, ok := s.Providers[providerID]; ok {
		return creds
	}
	return ReflectProviderCreds{
		APIKey:  s.APIKey,
		BaseURL: s.BaseURL,
	}
}

type ReflectStore interface {
	ListEnabledAgents(ctx context.Context) ([]ReflectAgent, error)
	Snapshot(ctx context.Context, agentID string) (*ReflectSnapshot, error)
}

type ReflectPlatform interface {
	ParentContext() context.Context
	Memory() memory.Provider
	Store() ReflectStore
	Workspace() string
	BuildStreamFunc(api, apiKey, baseURL string) (providers.StreamFunc, error)
}

func parseReflectModelRef(ref string) (provider, model string) {
	if provider, model, ok := strings.Cut(ref, "/"); ok {
		return provider, model
	}
	return "", ref
}
