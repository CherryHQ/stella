package ai

import (
	"math"
	"testing"
)

func TestModelImageCapability(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  ImageCapability
	}{
		{"declared with image", []string{"text", "image"}, ImageSupported},
		{"image only", []string{"image"}, ImageSupported},
		{"declared without image", []string{"text"}, ImageUnsupported},
		{"declared with other modalities", []string{"text", "audio"}, ImageUnsupported},
		{"nil input", nil, ImageUnknown},
		{"empty input", []string{}, ImageUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Model{Input: tt.input}).ImageCapability(); got != tt.want {
				t.Errorf("ImageCapability() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestImageUnknownIsZeroValue(t *testing.T) {
	var zero ImageCapability
	if zero != ImageUnknown {
		t.Errorf("zero ImageCapability = %v, want ImageUnknown", zero)
	}
}

func TestUsageWithCostRequiresReportedUsageAndConfiguredRates(t *testing.T) {
	rates := ModelCost{ModelRates: ModelRates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}, Priced: true}
	got := (Usage{Reported: true, InputTokens: 1_000_000, OutputTokens: 2_000_000, CacheRead: 3_000_000, CacheWrite: 4_000_000}).WithCost(rates)
	if !got.CostConfigured || got.Cost.Input != 3 || got.Cost.Output != 30 || got.Cost.CacheRead != 0.9 || got.Cost.CacheWrite != 15 || got.Cost.Total != 48.9 {
		t.Fatalf("priced usage = %+v", got)
	}
	if got := (Usage{InputTokens: 1}).WithCost(rates); got.CostConfigured {
		t.Fatal("unreported usage must stay unpriced")
	}
	if got := (Usage{Reported: true, InputTokens: 1}).WithCost(ModelCost{}); got.CostConfigured {
		t.Fatal("unconfigured rates must stay unpriced")
	}
}

// A provider that folds cache hits into its input count must not have the
// cached share billed twice: once at the input rate and again at the cache
// rate. This was inflating real session costs by several times.
func TestUsageWithCachedInputPricesEachTokenOnce(t *testing.T) {
	u := UsageWithCachedInput(1_000_000, 0, 900_000, 1_000_000)
	if u.InputTokens != 100_000 || u.CacheRead != 900_000 {
		t.Fatalf("categories overlap: %+v", u)
	}
	u.Reported = true
	priced := u.WithCost(ModelCost{ModelRates: ModelRates{Input: 1.0, CacheRead: 0.1}, Priced: true})
	if got, want := priced.Cost.Total, 0.19; math.Abs(got-want) > 1e-9 {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

// A malformed payload claiming more cache hits than input must not produce
// negative usage, which would show up as a negative cost.
func TestUsageWithCachedInputClampsAnImpossibleCacheCount(t *testing.T) {
	if u := UsageWithCachedInput(5, 1, 9, 6); u.InputTokens != 0 {
		t.Fatalf("input tokens = %d, want 0", u.InputTokens)
	}
}

func TestUsagePromptTokensIncludesAllDisjointInputCategories(t *testing.T) {
	for name, usage := range map[string]Usage{
		"openai cache read":        {InputTokens: 7, CacheRead: 5},
		"anthropic cache read":     {InputTokens: 7, CacheRead: 5},
		"anthropic cache creation": {InputTokens: 7, CacheWrite: 5},
	} {
		t.Run(name, func(t *testing.T) {
			if got := usage.PromptTokens(); got != 12 {
				t.Fatalf("PromptTokens() = %d, want 12", got)
			}
		})
	}
}

func TestModelCostSelectsWholeRequestContextTier(t *testing.T) {
	cost := ModelCost{
		ModelRates: ModelRates{Input: 5, Output: 30}, Priced: true,
		Tiers: []ModelCostTier{{MinContext: 272_000, ModelRates: ModelRates{Input: 10, Output: 45}}},
	}
	for prompt, want := range map[int]float64{271_999: 1.360025, 272_000: 2.720045} {
		usage := Usage{Reported: true, InputTokens: prompt, OutputTokens: 1}
		got := usage.WithCost(cost).Cost.Total
		if math.Abs(got-want) > 1e-12 {
			t.Errorf("prompt %d cost = %.12f, want %.12f", prompt, got, want)
		}
	}
}

func TestModelCostDistinguishesFreeFromUnknownAndClampsReasoning(t *testing.T) {
	free := (Usage{Reported: true, OutputTokens: 10, ReasoningTokens: 50}).WithCost(ModelCost{ModelRates: ModelRates{Output: 9}, Priced: true})
	if !free.CostConfigured || free.Cost.Total != 0 {
		t.Fatalf("free cost = %+v", free)
	}
	unknown := (Usage{Reported: true, OutputTokens: 10}).WithCost(ModelCost{ModelRates: ModelRates{Output: 9}})
	if unknown.CostConfigured {
		t.Fatalf("unknown model unexpectedly priced: %+v", unknown)
	}
	clamped := (Usage{Reported: true, OutputTokens: 10, ReasoningTokens: 50}).WithCost(ModelCost{ModelRates: ModelRates{Output: 9, Reasoning: 3}, Priced: true})
	if clamped.Cost.Total < 0 || clamped.Cost.Reasoning != 0.00003 {
		t.Fatalf("clamped cost = %+v", clamped.Cost)
	}
}
