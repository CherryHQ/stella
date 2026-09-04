package providers

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
)

// Config contains the deployment-owned inputs needed to build a provider.
type Config struct {
	APIKey  string
	BaseURL string
}

// Definition describes one compiled-in provider adapter.
type Definition struct {
	ID         string
	Name       string
	DefaultURL string
	Build      func(Config) (ProviderAdapter, error)
}

// Type is the control-plane projection of a provider definition.
type Type struct {
	ID         string
	Name       string
	DefaultURL string
}

// Registry is an immutable index of compiled-in provider definitions.
type Registry struct {
	definitions map[string]Definition
}

// NewRegistry validates and indexes provider definitions.
func NewRegistry(definitions ...Definition) (*Registry, error) {
	indexed := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		if definition.ID == "" {
			return nil, fmt.Errorf("providers: empty provider id")
		}
		if definition.Name == "" {
			return nil, fmt.Errorf("providers: empty name for %q", definition.ID)
		}
		if definition.Build == nil {
			return nil, fmt.Errorf("providers: nil builder for %q", definition.ID)
		}
		if _, exists := indexed[definition.ID]; exists {
			return nil, fmt.Errorf("providers: duplicate provider id %q", definition.ID)
		}
		indexed[definition.ID] = definition
	}
	return &Registry{definitions: indexed}, nil
}

// Types returns the registered provider types sorted by ID.
func (r *Registry) Types() []Type {
	if r == nil {
		return nil
	}
	types := make([]Type, 0, len(r.definitions))
	for definition := range maps.Values(r.definitions) {
		types = append(types, Type{
			ID:         definition.ID,
			Name:       definition.Name,
			DefaultURL: definition.DefaultURL,
		})
	}
	slices.SortFunc(types, func(a, b Type) int { return cmp.Compare(a.ID, b.ID) })
	return types
}

// Build constructs the registered provider with deployment-owned config.
func (r *Registry) Build(id string, config Config) (ProviderAdapter, error) {
	if r == nil {
		return nil, ErrProviderNotFound
	}
	definition, ok := r.definitions[id]
	if !ok {
		return nil, ErrProviderNotFound
	}
	return definition.Build(config)
}

// BuildStream constructs the registered provider and projects its stream function.
func (r *Registry) BuildStream(id string, config Config) (StreamFunc, error) {
	adapter, err := r.Build(id, config)
	if err != nil {
		return nil, err
	}
	return AdapterStreamFunc(adapter), nil
}
