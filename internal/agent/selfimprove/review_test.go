package selfimprove

import (
	"testing"

	"github.com/vaayne/anna/internal/config"
)

func TestConfigDefaults(t *testing.T) {
	t.Parallel()

	t.Run("Interval defaults to 1h", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{}
		if got := cfg.Interval(); got != "1h" {
			t.Errorf("Interval() = %q, want %q", got, "1h")
		}
	})

	t.Run("Interval uses configured value", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{Every: "30m"}
		if got := cfg.Interval(); got != "30m" {
			t.Errorf("Interval() = %q, want %q", got, "30m")
		}
	})

	t.Run("Batch defaults to 5", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{}
		if got := cfg.Batch(); got != 5 {
			t.Errorf("Batch() = %d, want %d", got, 5)
		}
	})

	t.Run("Batch uses configured value", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{BatchSize: 10}
		if got := cfg.Batch(); got != 10 {
			t.Errorf("Batch() = %d, want %d", got, 10)
		}
	})

	t.Run("Batch negative falls back to default", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{BatchSize: -1}
		if got := cfg.Batch(); got != 5 {
			t.Errorf("Batch() = %d, want %d", got, 5)
		}
	})

	t.Run("IsEnabled defaults to false", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{}
		if cfg.IsEnabled() {
			t.Error("IsEnabled() = true, want false")
		}
	})

	t.Run("IsEnabled true when set", func(t *testing.T) {
		t.Parallel()
		enabled := true
		cfg := config.SelfImproveConfig{Enabled: &enabled}
		if !cfg.IsEnabled() {
			t.Error("IsEnabled() = false, want true")
		}
	})
}

// Tests for buildConversationText were removed — that function was replaced
// by ReviewSource.BuildReviewContext, tested in plugins/memory/lcm/.
