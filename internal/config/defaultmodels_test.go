package config

import (
	"context"
	"testing"
)

// fakeModelStore adds a provider catalog to the setting store so embedding
// resolution can walk from a model ref to its credentials.
type fakeModelStore struct {
	fakeSettingStore
	providers []Provider
}

func (f *fakeModelStore) ListProviders(_ context.Context) ([]Provider, error) {
	return f.providers, nil
}

func newModelStore(settings map[string]string, providers ...Provider) *fakeModelStore {
	return &fakeModelStore{fakeSettingStore: fakeSettingStore{m: settings}, providers: providers}
}

func TestLoadDefaultModels_UnsetMeansEveryRoleUnset(t *testing.T) {
	got, err := LoadDefaultModels(context.Background(), &fakeSettingStore{m: map[string]string{}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != (DefaultModels{}) {
		t.Errorf("got %+v, want the zero value for a never-configured deployment", got)
	}
}

func TestDefaultModels_RoundTripTrimsRefs(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{}}
	if err := SaveDefaultModels(context.Background(), st, DefaultModels{
		Model:          "  openai/gpt-5 \n",
		ModelVision:    " openai/gpt-4o ",
		ModelEmbedding: " openai/text-embedding-3-small ",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadDefaultModels(context.Background(), st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := DefaultModels{
		Model:          "openai/gpt-5",
		ModelVision:    "openai/gpt-4o",
		ModelEmbedding: "openai/text-embedding-3-small",
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLoadDefaultModels_MalformedJSONIsAnError(t *testing.T) {
	st := &fakeSettingStore{m: map[string]string{DefaultModelsSettingKey: `{"model":`}}
	if _, err := LoadDefaultModels(context.Background(), st); err == nil {
		t.Fatal("expected an error for a corrupt setting row")
	}
}

func TestMergeAgentModels_OverridesFieldByField(t *testing.T) {
	def := DefaultModels{
		Model:               "openai/gpt-5",
		ModelThinking:       "medium",
		ModelStrong:         "openai/gpt-5-pro",
		ModelStrongThinking: "high",
		ModelFast:           "openai/gpt-5-mini",
		ModelFastThinking:   "low",
	}
	// Only two fields are overridden: everything else must survive from the
	// deployment defaults rather than being blanked by the agent's empty columns.
	got := MergeAgentModels(def, Agent{Model: "anthropic/claude-sonnet-4-6", ModelFastThinking: "minimal"})
	want := AgentModels{
		Model:               "anthropic/claude-sonnet-4-6",
		ModelThinking:       "medium",
		ModelStrong:         "openai/gpt-5-pro",
		ModelStrongThinking: "high",
		ModelFast:           "openai/gpt-5-mini",
		ModelFastThinking:   "minimal",
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestValidModelRef(t *testing.T) {
	for _, ref := range []string{"", "openai/gpt-4o", "openrouter/anthropic/claude"} {
		if !ValidModelRef(ref) {
			t.Errorf("ref %q rejected, want accepted", ref)
		}
	}
	// A half-typed ref resolves to an empty model id at runtime, which asks a
	// provider for no model at all. The write path is the last place to catch it.
	for _, ref := range []string{"gpt-4o", "openai/", "/gpt-4o"} {
		if ValidModelRef(ref) {
			t.Errorf("ref %q accepted, want rejected", ref)
		}
	}
}

func TestResolveEmbedding_TakesCredentialsFromTheModelsProvider(t *testing.T) {
	st := newModelStore(map[string]string{
		EmbeddingSettingKey:     `{"enabled":true,"dim":512,"normalize":true,"model":"legacy-model","api_key":"sk-legacy","base_url":"https://legacy"}`,
		DefaultModelsSettingKey: `{"model_embedding":"prov-1/text-embedding-3-large"}`,
	}, Provider{ID: "prov-1", Type: "openai", APIKey: "sk-provider", BaseURL: "https://provider"})

	got, err := ResolveEmbedding(context.Background(), st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Model != "text-embedding-3-large" {
		t.Errorf("Model = %q, want the bare model half of the ref", got.Model)
	}
	if got.APIKey != "sk-provider" || got.BaseURL != "https://provider" {
		t.Errorf("credentials = %q/%q, want the provider's, not the legacy block's", got.APIKey, got.BaseURL)
	}
	if !got.Enabled || got.Dim != 512 || !got.Normalize {
		t.Errorf("lane knobs lost: %+v", got)
	}
}

// A ref whose provider half names a provider type resolves through the same
// single-provider alias the snapshot credential lookup uses.
func TestResolveEmbedding_ResolvesUniqueProviderTypeAlias(t *testing.T) {
	st := newModelStore(map[string]string{
		DefaultModelsSettingKey: `{"model_embedding":"openai/text-embedding-3-small"}`,
	}, Provider{ID: "prov-1", Type: "openai", APIKey: "sk-provider"})

	got, err := ResolveEmbedding(context.Background(), st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.APIKey != "sk-provider" {
		t.Errorf("APIKey = %q, want the aliased provider's key", got.APIKey)
	}
}

// A deployment the migration could not match to a provider keeps running on its
// legacy block until an admin names an embedding model.
func TestResolveEmbedding_FallsBackToLegacyBlockWhenNoRefIsSet(t *testing.T) {
	st := newModelStore(map[string]string{
		EmbeddingSettingKey: `{"enabled":true,"model":"legacy-model","api_key":"sk-legacy","base_url":"https://legacy"}`,
	})

	got, err := ResolveEmbedding(context.Background(), st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Model != "legacy-model" || got.APIKey != "sk-legacy" || got.BaseURL != "https://legacy" {
		t.Errorf("got %+v, want the legacy embedding block verbatim", got)
	}
}

// Falling back to the legacy key here would embed against a different account and
// mix two models' geometry into one vector space. No credentials is the safe,
// visible failure.
func TestResolveEmbedding_DanglingRefYieldsNoCredentials(t *testing.T) {
	st := newModelStore(map[string]string{
		EmbeddingSettingKey:     `{"enabled":true,"model":"legacy-model","api_key":"sk-legacy"}`,
		DefaultModelsSettingKey: `{"model_embedding":"deleted-provider/text-embedding-3-small"}`,
	})

	got, err := ResolveEmbedding(context.Background(), st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.APIKey != "" {
		t.Errorf("APIKey = %q, want empty so the lane reports itself disabled", got.APIKey)
	}
}
