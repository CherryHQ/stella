package agent

import (
	"os"
	"testing"

	"github.com/CherryHQ/stella/pkg/providers"
	anthropicprovider "github.com/CherryHQ/stella/plugins/providers/anthropic"
)

func skipWithoutAnthropicKey(t *testing.T) {
	t.Helper()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping integration test")
	}
}

func integrationProviderStreamBuilder(api, apiKey, baseURL string) (providers.StreamFunc, error) {
	if api != "anthropic" {
		return nil, providers.ErrProviderNotFound
	}
	adapter := anthropicprovider.New(anthropicprovider.Config{
		APIKey:  apiKey,
		BaseURL: baseURL,
	})
	return providers.AdapterStreamFunc(adapter), nil
}
