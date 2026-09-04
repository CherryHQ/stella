package main

import (
	"reflect"
	"testing"

	"github.com/CherryHQ/stella/pkg/providers"
)

func TestSetupProviderRegistry(t *testing.T) {
	registry, err := setupProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []providers.Type{
		{ID: "anthropic", Name: "Anthropic", DefaultURL: "https://api.anthropic.com"},
		{ID: "openai", Name: "OpenAI", DefaultURL: "https://api.openai.com/v1"},
		{ID: "openai-response", Name: "OpenAI Response", DefaultURL: "https://api.openai.com/v1"},
	}
	if got := registry.Types(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Types() = %#v, want %#v", got, want)
	}
}
