package pluginproviders_test

import (
	"context"
	"testing"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/providers"
	pluginproviders "github.com/vaayne/anna/plugins/providers"
)

// fakeAdapter satisfies providers.ProviderAdapter.
type fakeAdapter struct{ api string }

func (f *fakeAdapter) API() string { return f.api }
func (f *fakeAdapter) Stream(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, nil
}
func (f *fakeAdapter) StreamSimple(_ context.Context, _ ai.Model, _ ai.Context, _ ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return nil, nil
}

func TestProviderRegisterAndNames(t *testing.T) {
	pluginproviders.Register("test-provider", pluginproviders.Registration{
		Factory: func(_ pluginproviders.ProviderConfig) (providers.ProviderAdapter, error) {
			return &fakeAdapter{api: "test-provider"}, nil
		},
		Meta: pluginproviders.ProviderMeta{Name: "Test Provider", DefaultURL: "http://test"},
	})

	names := pluginproviders.Names()
	var found bool
	for _, n := range names {
		if n == "test-provider" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'test-provider' in Names()")
	}
}

func TestProviderMetas(t *testing.T) {
	pluginproviders.Register("meta-provider", pluginproviders.Registration{
		Factory: func(_ pluginproviders.ProviderConfig) (providers.ProviderAdapter, error) {
			return &fakeAdapter{api: "meta-provider"}, nil
		},
		Meta: pluginproviders.ProviderMeta{Name: "Meta Provider", DefaultURL: "http://meta"},
	})

	metas := pluginproviders.Metas()
	if _, ok := metas["meta-provider"]; !ok {
		t.Error("expected 'meta-provider' in Metas()")
	}
}

func TestProviderBuild_Success(t *testing.T) {
	pluginproviders.Register("buildable-provider", pluginproviders.Registration{
		Factory: func(_ pluginproviders.ProviderConfig) (providers.ProviderAdapter, error) {
			return &fakeAdapter{api: "buildable-provider"}, nil
		},
	})

	adapter, err := pluginproviders.Build("buildable-provider", pluginproviders.ProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.API() != "buildable-provider" {
		t.Errorf("unexpected API: %q", adapter.API())
	}
}

func TestProviderBuild_Unknown(t *testing.T) {
	_, err := pluginproviders.Build("nonexistent-provider", pluginproviders.ProviderConfig{})
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestProviderBuildRegistry(t *testing.T) {
	pluginproviders.Register("registry-provider", pluginproviders.Registration{
		Factory: func(_ pluginproviders.ProviderConfig) (providers.ProviderAdapter, error) {
			return &fakeAdapter{api: "registry-provider"}, nil
		},
	})

	reg, err := pluginproviders.BuildRegistry("registry-provider", pluginproviders.ProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("registry-provider"); !ok {
		t.Error("expected 'registry-provider' in built registry")
	}
}
