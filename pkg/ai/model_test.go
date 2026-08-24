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
	rates := ModelCost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
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
	priced := u.WithCost(ModelCost{Input: 1.0, CacheRead: 0.1})
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
