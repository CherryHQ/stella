package ai

import "testing"

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
