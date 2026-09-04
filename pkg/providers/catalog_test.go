package providers

import (
	"errors"
	"reflect"
	"testing"
)

func TestNewRegistryRejectsInvalidDefinitions(t *testing.T) {
	build := func(Config) (ProviderAdapter, error) { return nil, nil }
	tests := []struct {
		name        string
		definitions []Definition
	}{
		{name: "empty id", definitions: []Definition{{Name: "Provider", Build: build}}},
		{name: "empty name", definitions: []Definition{{ID: "provider", Build: build}}},
		{name: "nil builder", definitions: []Definition{{ID: "provider", Name: "Provider"}}},
		{name: "duplicate id", definitions: []Definition{{ID: "provider", Name: "One", Build: build}, {ID: "provider", Name: "Two", Build: build}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRegistry(tt.definitions...); err == nil {
				t.Fatal("NewRegistry returned nil error")
			}
		})
	}
}

func TestRegistryTypesAreSorted(t *testing.T) {
	build := func(Config) (ProviderAdapter, error) { return nil, nil }
	registry, err := NewRegistry(
		Definition{ID: "zeta", Name: "Zeta", DefaultURL: "https://zeta.example", Build: build},
		Definition{ID: "alpha", Name: "Alpha", DefaultURL: "https://alpha.example", Build: build},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []Type{
		{ID: "alpha", Name: "Alpha", DefaultURL: "https://alpha.example"},
		{ID: "zeta", Name: "Zeta", DefaultURL: "https://zeta.example"},
	}
	if got := registry.Types(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Types() = %#v, want %#v", got, want)
	}
}

func TestRegistryBuild(t *testing.T) {
	wantErr := errors.New("build failed")
	var got Config
	registry, err := NewRegistry(Definition{
		ID:   "provider",
		Name: "Provider",
		Build: func(config Config) (ProviderAdapter, error) {
			got = config
			return nil, wantErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{APIKey: "key", BaseURL: "https://example.com"}
	if _, err := registry.Build("provider", want); !errors.Is(err, wantErr) {
		t.Fatalf("Build() error = %v, want %v", err, wantErr)
	}
	if got != want {
		t.Fatalf("Build() config = %#v, want %#v", got, want)
	}
	if _, err := registry.Build("missing", Config{}); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("Build(missing) error = %v, want %v", err, ErrProviderNotFound)
	}
}
