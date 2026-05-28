package reflect

import (
	"context"
	"strings"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	ModelTierStrong = "strong"
	ModelTierFast   = "fast"
)

// ProviderCreds bundles the credentials used to call a single model provider.
type ProviderCreds struct {
	APIKey  string
	BaseURL string
}

// Agent identifies an agent eligible for reflection.
type Agent struct {
	ID string
}

// Snapshot is the per-agent view reflect needs to run a review cycle.
type Snapshot struct {
	AgentID      string
	Provider     string
	Model        string
	ModelStrong  string
	ModelFast    string
	Workspace    string
	APIKey       string
	BaseURL      string
	SystemPrompt string
	Providers    map[string]ProviderCreds
}

// ResolveModelID returns the model identifier for the requested tier,
// falling back to the snapshot's default model when the tier-specific
// override is empty.
func (s *Snapshot) ResolveModelID(tier string) string {
	switch tier {
	case ModelTierFast:
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

// ResolveModelTier returns the resolved ai.Model for the requested tier.
func (s *Snapshot) ResolveModelTier(tier string) ai.Model {
	modelRef := s.ResolveModelID(tier)
	providerID, modelID := parseModelRef(modelRef)
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

// ResolveProviderCreds returns the credentials configured for the given
// provider, falling back to the snapshot's default credentials.
func (s *Snapshot) ResolveProviderCreds(providerID string) ProviderCreds {
	if providerID == "" {
		providerID = s.Provider
	}
	if creds, ok := s.Providers[providerID]; ok {
		return creds
	}
	return ProviderCreds{
		APIKey:  s.APIKey,
		BaseURL: s.BaseURL,
	}
}

// Store is the subset of config the reflect Service needs.
type Store interface {
	ListEnabledAgents(ctx context.Context) ([]Agent, error)
	Snapshot(ctx context.Context, agentID string) (*Snapshot, error)
}

// NewConfigStore adapts a config.Store into the reflect Store interface.
func NewConfigStore(store config.Store) Store {
	return configStore{store: store}
}

type configStore struct {
	store config.Store
}

func (s configStore) ListEnabledAgents(ctx context.Context) ([]Agent, error) {
	agents, err := s.store.ListEnabledAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(agents))
	for _, agent := range agents {
		out = append(out, Agent{ID: agent.ID})
	}
	return out, nil
}

func (s configStore) Snapshot(ctx context.Context, agentID string) (*Snapshot, error) {
	snap, err := s.store.Snapshot(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := &Snapshot{
		AgentID:      snap.AgentID,
		Provider:     snap.Provider,
		Model:        snap.Model,
		ModelStrong:  snap.ModelStrong,
		ModelFast:    snap.ModelFast,
		Workspace:    snap.Workspace,
		APIKey:       snap.APIKey,
		BaseURL:      snap.BaseURL,
		SystemPrompt: snap.SystemPrompt,
		Providers:    map[string]ProviderCreds{},
	}
	for providerID, creds := range snap.Providers {
		out.Providers[providerID] = ProviderCreds{
			APIKey:  creds.APIKey,
			BaseURL: creds.BaseURL,
		}
	}
	return out, nil
}

func parseModelRef(ref string) (provider, model string) {
	if provider, model, ok := strings.Cut(ref, "/"); ok {
		return provider, model
	}
	return "", ref
}
