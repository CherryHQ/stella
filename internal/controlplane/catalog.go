package controlplane

import (
	"context"
	"sort"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
)

// Non-admin catalog reads.
//
// The methods below are catalog data every signed-in user needs to start a chat
// (the enabled model list and the user-facing channel list), so they are not
// gated by the admin Begin. They are still authorization-gated: each validates a
// trusted authz.Authority and fails closed — refusing to touch the store — before
// any read, so an unauthenticated or non-user actor never reaches durable state.
// They read the same control-plane config the admin methods own but never mutate
// and never expose admin-only secrets: PublicChannels returns the secret-free
// PublicChannel projection, not the raw config.Channel (whose Config JSON carries
// channel credentials). A nil receiver fails closed with ErrUnavailable.

// PublicChannel is the config-secret-free projection of a channel for the
// user-facing channel list. It deliberately omits the raw Config JSON that
// config.Channel carries (channel credentials), exposing only the fields the
// public list renders.
type PublicChannel struct {
	ID      string
	Type    string // effective type: the configured type, or the id when unset
	AgentID string
	Enabled bool
}

// requireCatalogReader fails closed unless the authority is a valid user/admin
// principal (an admin is a user actor with the admin flag). The catalog is
// visible to any signed-in user, but the domain refuses to read the store for an
// unauthenticated or non-user actor.
func requireCatalogReader(authority authz.Authority) error {
	if !authority.Valid() || authority.Kind() != authz.ActorUser {
		return authz.ErrForbidden
	}
	return nil
}

// ListEnabledModels returns the models available for selection: enabled models
// from enabled providers, plus fetched-cache models whose provider is enabled,
// deduplicated by provider/model and sorted. It performs no provider API calls.
func (s *Service) ListEnabledModels(ctx context.Context, authority authz.Authority) ([]config.CachedModel, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	if err := requireCatalogReader(authority); err != nil {
		return nil, err
	}
	providers, err := s.store.ListProviders(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	filtered := make([]config.CachedModel, 0)
	providerByID := make(map[string]config.Provider, len(providers))
	modelEnabled := make(map[string]map[string]bool, len(providers))
	add := func(providerID, modelID string, enabled map[string]bool) {
		if providerID == "" || modelID == "" {
			return
		}
		provider, ok := providerByID[providerID]
		if !ok || !provider.Enabled {
			return
		}
		if value, ok := enabled[modelID]; ok && !value {
			return
		}
		key := providerID + "/" + modelID
		if seen[key] {
			return
		}
		seen[key] = true
		filtered = append(filtered, config.CachedModel{Provider: providerID, ProviderName: provider.Name, Model: modelID})
	}
	for _, provider := range providers {
		providerByID[provider.ID] = provider
		enabled := make(map[string]bool, len(provider.Models))
		for modelID, model := range provider.Models {
			enabled[modelID] = model.Enabled
			add(provider.ID, modelID, enabled)
		}
		modelEnabled[provider.ID] = enabled
	}

	if cached, err := s.store.ListCachedModels(ctx); err == nil {
		for _, model := range cached {
			add(model.Provider, model.Model, modelEnabled[model.Provider])
		}
	} else {
		s.log.Warn("failed to load cached models", "error", err)
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Provider != filtered[j].Provider {
			return filtered[i].Provider < filtered[j].Provider
		}
		return filtered[i].Model < filtered[j].Model
	})
	return filtered, nil
}

// PublicChannels returns the secret-free projection of every configured channel
// for the user-facing channel list. The transport filters visibility by the
// caller's agent access and the enabled plugin types; this only supplies the
// projected rows.
func (s *Service) PublicChannels(ctx context.Context, authority authz.Authority) ([]PublicChannel, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	if err := requireCatalogReader(authority); err != nil {
		return nil, err
	}
	channels, err := s.store.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PublicChannel, 0, len(channels))
	for _, ch := range channels {
		out = append(out, PublicChannel{
			ID:      ch.ID,
			Type:    effectiveChannelType(ch),
			AgentID: ch.AgentID,
			Enabled: ch.Enabled,
		})
	}
	return out, nil
}

// DisabledChannelTypes returns the set of channel-plugin names an admin has
// explicitly switched off, used by the transport to hide their channels.
//
// A platform is usable unless an admin explicitly switched it off, so this reads
// the stored override rows rather than the merged plugin list: the builtin
// default for a channel plugin is "disabled" (a platform with no channel has
// nothing to run), and treating that default as a veto would hide every channel
// whose plugin row nobody flipped on. The runtime applies the same rule to
// instances (RuntimeHost.channelPlatformDisabled), so creating a channel needs
// no plugin row at all.
func (s *Service) DisabledChannelTypes(ctx context.Context, authority authz.Authority) (map[string]bool, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	if err := requireCatalogReader(authority); err != nil {
		return nil, err
	}
	overrides, err := s.store.ListPluginOverrides(ctx)
	if err != nil {
		return nil, err
	}
	disabled := make(map[string]bool, len(overrides))
	for _, override := range overrides {
		if override.Kind == config.PluginKindChannel && !override.Enabled {
			disabled[override.Name] = true
		}
	}
	return disabled, nil
}

// effectiveChannelType resolves a channel's type, falling back to its id when the
// type is unset (the legacy singleton channels stored the type in the id).
func effectiveChannelType(ch config.Channel) string {
	if ch.Type != "" {
		return ch.Type
	}
	return ch.ID
}
