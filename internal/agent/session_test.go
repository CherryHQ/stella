package agent

import "testing"

func TestCompactionConfigWithDefaults(t *testing.T) {
	tests := []struct {
		name          string
		input         CompactionConfig
		wantMaxTokens int
		wantKeepTail  int
	}{
		{"zero values get defaults", CompactionConfig{}, 80_000, 20},
		{"custom values preserved", CompactionConfig{MaxTokens: 50_000, KeepTail: 10}, 50_000, 10},
		{"negative MaxTokens preserved", CompactionConfig{MaxTokens: -1}, -1, 20},
		{"partial override", CompactionConfig{KeepTail: 5}, 80_000, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.WithDefaults()
			if got.MaxTokens != tt.wantMaxTokens {
				t.Errorf("MaxTokens = %d, want %d", got.MaxTokens, tt.wantMaxTokens)
			}
			if got.KeepTail != tt.wantKeepTail {
				t.Errorf("KeepTail = %d, want %d", got.KeepTail, tt.wantKeepTail)
			}
		})
	}
}
