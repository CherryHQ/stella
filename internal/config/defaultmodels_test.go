package config

import (
	"context"
	"strings"
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

func TestValidateDefaultModelsRejectsOverlongReference(t *testing.T) {
	overlong := "provider/" + strings.Repeat("m", MaxDefaultModelRefBytes)
	field, value, isModel, ok := ValidateDefaultModels(DefaultModels{Model: overlong})
	if ok || !isModel || field != "model" || value != overlong {
		t.Fatalf("ValidateDefaultModels overlong = (%q, %q, %t, %t), want model validation failure", field, value, isModel, ok)
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
