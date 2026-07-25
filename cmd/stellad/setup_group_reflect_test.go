package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
)

type fakeGroupReflectProviderStore struct {
	providers []config.Provider
	err       error
}

func (s fakeGroupReflectProviderStore) ListProviders(context.Context) ([]config.Provider, error) {
	return append([]config.Provider(nil), s.providers...), s.err
}

type structuredGroupMemoryProvider struct {
	memory.Provider
}

type groupFactOnlyWrapper struct {
	memory.Provider
	inner *structuredGroupMemoryProvider
}

func (w *groupFactOnlyWrapper) Unwrap() memory.Provider {
	return w.inner
}

func (w *groupFactOnlyWrapper) ListActiveGroupFacts(ctx context.Context, groupID string) ([]memory.GroupFact, error) {
	return w.inner.ListActiveGroupFacts(ctx, groupID)
}

func (w *groupFactOnlyWrapper) GetGroupFactVersion(ctx context.Context, groupID string) (int64, error) {
	return w.inner.GetGroupFactVersion(ctx, groupID)
}

func (w *groupFactOnlyWrapper) ListGroupActorDisplayNames(ctx context.Context, groupID string) ([]memory.GroupActorDisplayName, error) {
	return w.inner.ListGroupActorDisplayNames(ctx, groupID)
}

type groupFactEventWrapper struct {
	*groupFactOnlyWrapper
}

func (w *groupFactEventWrapper) SyncGroupEventsBefore(ctx context.Context, session memory.Session, seq int64) error {
	return w.inner.SyncGroupEventsBefore(ctx, session, seq)
}

func (w *groupFactEventWrapper) AppendGroupTurn(
	ctx context.Context,
	session memory.Session,
	groupMessageID string,
	trigger ai.Message,
	continuation ...ai.Message,
) error {
	return w.inner.AppendGroupTurn(ctx, session, groupMessageID, trigger, continuation...)
}

func (*structuredGroupMemoryProvider) ListActiveGroupFacts(context.Context, string) ([]memory.GroupFact, error) {
	return nil, nil
}

func (*structuredGroupMemoryProvider) GetGroupFactVersion(context.Context, string) (int64, error) {
	return 0, nil
}

func (*structuredGroupMemoryProvider) ListGroupActorDisplayNames(context.Context, string) ([]memory.GroupActorDisplayName, error) {
	return nil, nil
}

func (*structuredGroupMemoryProvider) SyncGroupEventsBefore(context.Context, memory.Session, int64) error {
	return nil
}

func (*structuredGroupMemoryProvider) AppendGroupTurn(
	context.Context,
	memory.Session,
	string,
	ai.Message,
	...ai.Message,
) error {
	return nil
}

func (*structuredGroupMemoryProvider) CommitGroupCursor(context.Context, memory.Session, int64) error {
	return nil
}

func TestResolveGroupMemoryMode(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "", want: groupMemoryModeLegacy},
		{raw: " Legacy ", want: groupMemoryModeLegacy},
		{raw: "STRUCTURED", want: groupMemoryModeStructured},
		{raw: "both", wantErr: true},
	} {
		got, err := resolveGroupMemoryMode(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Fatalf("resolveGroupMemoryMode(%q) error = %v, wantErr %t", tc.raw, err, tc.wantErr)
		}
		if got != tc.want {
			t.Fatalf("resolveGroupMemoryMode(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestResolveGroupReflectSchedule(t *testing.T) {
	if got := resolveGroupReflectSchedule(""); got.Cron != groupReflectDefaultCron || got.Every != "" {
		t.Fatalf("default schedule = %#v", got)
	}
	if got := resolveGroupReflectSchedule("2h"); got.Every != "2h0m0s" || got.Cron != "" {
		t.Fatalf("override schedule = %#v", got)
	}
	if got := resolveGroupReflectInterval("1s"); got != minGroupReflectInterval {
		t.Fatalf("clamped interval = %s, want %s", got, minGroupReflectInterval)
	}
	if got := resolveGroupReflectInterval("garbage"); got != 6*time.Hour {
		t.Fatalf("invalid interval = %s, want 6h", got)
	}
}

func TestResolveGroupReflectModel(t *testing.T) {
	valid := config.Provider{
		ID:      "provider-1",
		Type:    "openai",
		Enabled: true,
		APIKey:  "secret",
		Models: map[string]config.ProviderModel{
			"group-model": {
				Name:          "Group Model",
				Enabled:       true,
				ContextWindow: config.GroupMemoryMinimumContextWindow,
				MaxTokens:     16_000,
			},
		},
	}

	model, provider, err := resolveGroupReflectModel(
		context.Background(),
		fakeGroupReflectProviderStore{providers: []config.Provider{valid}},
		"provider-1/group-model",
	)
	if err != nil {
		t.Fatalf("resolve valid model: %v", err)
	}
	if provider.ID != valid.ID || model.ID != "group-model" || model.ContextWindow != config.GroupMemoryMinimumContextWindow {
		t.Fatalf("resolved model/provider = %#v / %#v", model, provider)
	}

	model, provider, err = resolveGroupReflectModel(
		context.Background(),
		fakeGroupReflectProviderStore{providers: []config.Provider{valid}},
		"openai/group-model",
	)
	if err != nil || provider.ID != valid.ID || model.API != "openai" {
		t.Fatalf("unique provider-type alias failed: model=%#v provider=%#v err=%v", model, provider, err)
	}
}

func TestResolveGroupReflectModelFailsClosed(t *testing.T) {
	base := config.Provider{
		ID:      "provider-1",
		Type:    "openai",
		Enabled: true,
		APIKey:  "secret",
		Models: map[string]config.ProviderModel{
			"group-model": {
				Enabled:       true,
				ContextWindow: config.GroupMemoryMinimumContextWindow,
			},
		},
	}

	tests := []struct {
		name      string
		ref       string
		providers []config.Provider
		want      string
	}{
		{name: "malformed ref", ref: "group-model", providers: []config.Provider{base}, want: "provider/model"},
		{name: "unknown provider", ref: "missing/group-model", providers: []config.Provider{base}, want: "missing or disabled"},
		{name: "disabled provider", ref: "provider-1/group-model", providers: []config.Provider{func() config.Provider {
			p := base
			p.Enabled = false
			return p
		}()}, want: "missing or disabled"},
		{name: "missing credential", ref: "provider-1/group-model", providers: []config.Provider{func() config.Provider {
			p := base
			p.APIKey = ""
			return p
		}()}, want: "no API key"},
		{name: "unknown model", ref: "provider-1/missing", providers: []config.Provider{base}, want: "missing or disabled"},
		{name: "small context", ref: "provider-1/group-model", providers: []config.Provider{func() config.Provider {
			p := base
			p.Models = map[string]config.ProviderModel{
				"group-model": {Enabled: true, ContextWindow: config.GroupMemoryMinimumContextWindow - 1},
			}
			return p
		}()}, want: "requires at least"},
		{name: "ambiguous type", ref: "openai/group-model", providers: []config.Provider{
			base,
			func() config.Provider {
				p := base
				p.ID = "provider-2"
				return p
			}(),
		}, want: "ambiguous"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolveGroupReflectModel(
				context.Background(),
				fakeGroupReflectProviderStore{providers: tc.providers},
				tc.ref,
			)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want text %q", err, tc.want)
			}
		})
	}
}

func TestSetupGroupMemoryLegacyNeedsNoStructuredDependencies(t *testing.T) {
	got, err := setupGroupMemory(context.Background(), nil, nil, nil, nil, nil, config.GroupMemoryConfig{})
	if err != nil {
		t.Fatalf("legacy setup: %v", err)
	}
	if got.structured || got.promptLoader != nil {
		t.Fatalf("legacy setup = %#v", got)
	}
}

func TestSetupGroupMemoryStructuredRequiresAllDependencies(t *testing.T) {
	_, err := setupGroupMemory(
		context.Background(),
		nil,
		nil,
		nil,
		nil,
		nil,
		config.GroupMemoryConfig{Mode: "structured"},
	)
	if err == nil || !strings.Contains(err.Error(), "requires scheduler") {
		t.Fatalf("error = %v", err)
	}
}

func TestRequireStructuredGroupMemoryCapabilitiesChecksUnwrappedProvider(t *testing.T) {
	unsupported := memory.WithTracing(memorytest.New(), nil)
	if _, err := requireStructuredGroupMemoryCapabilities(unsupported); err == nil ||
		!strings.Contains(err.Error(), "Group Fact reads") {
		t.Fatalf("unsupported wrapped provider error = %v", err)
	}

	inner := &structuredGroupMemoryProvider{Provider: memorytest.New()}
	wrapped := memory.WithTracing(inner, nil)
	store, err := requireStructuredGroupMemoryCapabilities(wrapped)
	if err != nil {
		t.Fatalf("supported wrapped provider: %v", err)
	}
	if any(store) != any(wrapped) {
		t.Fatal("capability check should retain the tracing wrapper")
	}

	factOnly := &groupFactOnlyWrapper{Provider: inner, inner: inner}
	if _, err := requireStructuredGroupMemoryCapabilities(factOnly); err == nil ||
		!strings.Contains(err.Error(), "wrapper does not expose group event ingestion") {
		t.Fatalf("fact-only wrapper error = %v", err)
	}

	factAndEvent := &groupFactEventWrapper{groupFactOnlyWrapper: factOnly}
	if _, err := requireStructuredGroupMemoryCapabilities(factAndEvent); err == nil ||
		!strings.Contains(err.Error(), "wrapper does not expose group cursor commits") {
		t.Fatalf("fact/event wrapper error = %v", err)
	}
}
