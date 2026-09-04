package agent

import (
	"context"
	"os"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	anthropicprovider "github.com/CherryHQ/stella/plugins/providers/anthropic"
	nonebackend "github.com/CherryHQ/stella/plugins/sandbox/none"
)

func testSandboxBackends(t *testing.T) *sandbox.BackendRegistry {
	t.Helper()
	registry, err := sandbox.NewBackendRegistry(sandbox.BackendDefinition{
		Name: config.SandboxBackendNone,
		Create: func(ctx context.Context, request sandbox.BackendRequest) (pkgsandbox.Session, error) {
			return nonebackend.NewFactoryWithMountSources(request.MountSources, nonebackend.Config{StellaHome: request.Paths.StellaHome}).CreateSession(ctx, request.Policy)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

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
