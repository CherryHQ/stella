package channel

import (
	"strings"
	"testing"
	"time"
)

func TestSplitMessage(t *testing.T) {
	t.Run("short message", func(t *testing.T) {
		chunks := SplitMessage("hello", 100)
		if len(chunks) != 1 || chunks[0] != "hello" {
			t.Errorf("got %v", chunks)
		}
	})

	t.Run("splits at newline", func(t *testing.T) {
		text := strings.Repeat("a", 50) + "\n" + strings.Repeat("b", 50)
		chunks := SplitMessage(text, 60)
		if len(chunks) != 2 {
			t.Fatalf("got %d chunks, want 2", len(chunks))
		}
	})

	t.Run("handles UTF-8", func(t *testing.T) {
		// Each emoji is 4 bytes. Create a string that would split mid-rune.
		text := strings.Repeat("\U0001F600", 30) // 120 bytes
		chunks := SplitMessage(text, 50)
		for _, c := range chunks {
			for i := 0; i < len(c); {
				r, size := []rune(c[i:])[0], len(string([]rune(c[i:])[0]))
				if r == 0xFFFD {
					t.Error("invalid UTF-8 in chunk")
				}
				i += size
			}
		}
	})
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "1.5s"},
		{90 * time.Second, "1m30s"},
	}
	for _, tt := range tests {
		got := FormatDuration(tt.d)
		if got != tt.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
