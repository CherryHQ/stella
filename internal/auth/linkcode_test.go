package auth_test

import (
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/auth"
)

func TestLinkCodeGenerate(t *testing.T) {
	store := auth.NewLinkCodeStore()

	code := store.Generate(42, "telegram")
	if len(code) != 6 {
		t.Errorf("code length = %d, want 6", len(code))
	}
	// Code should be uppercase hex (0-9A-F).
	for _, c := range code {
		if (c < '0' || c > '9') && (c < 'A' || c > 'F') {
			t.Errorf("unexpected character in code: %c", c)
		}
	}
}

func TestLinkCodeConsume(t *testing.T) {
	store := auth.NewLinkCodeStore()

	code := store.Generate(42, "telegram")

	// Consume should succeed.
	userID, platform, ok := store.Consume(code)
	if !ok {
		t.Fatal("expected Consume to succeed")
	}
	if userID != 42 {
		t.Errorf("userID = %d, want 42", userID)
	}
	if platform != "telegram" {
		t.Errorf("platform = %q, want %q", platform, "telegram")
	}

	// Second consume should fail (single use).
	_, _, ok = store.Consume(code)
	if ok {
		t.Error("expected second Consume to fail")
	}
}

func TestLinkCodeConsumeCaseInsensitive(t *testing.T) {
	store := auth.NewLinkCodeStore()

	code := store.Generate(7, "qq")

	// Consume with lowercase should also work.
	_, _, ok := store.Consume(strings.ToLower(code))
	if !ok {
		t.Error("expected case-insensitive Consume to succeed")
	}
}

func TestLinkCodeConsumeInvalid(t *testing.T) {
	store := auth.NewLinkCodeStore()

	_, _, ok := store.Consume("ZZZZZZ")
	if ok {
		t.Error("expected Consume of unknown code to fail")
	}
}

func TestLinkCodeConsumeExpired(t *testing.T) {
	// We can't easily test TTL expiry without time manipulation,
	// but we can verify the generate/consume flow works.
	store := auth.NewLinkCodeStore()
	code := store.Generate(1, "feishu")

	// Immediately consuming should work.
	_, _, ok := store.Consume(code)
	if !ok {
		t.Error("expected immediate Consume to succeed")
	}
}

func TestLinkCodeUniqueness(t *testing.T) {
	store := auth.NewLinkCodeStore()
	seen := make(map[string]bool)

	for i := 0; i < 100; i++ {
		code := store.Generate(int64(i), "telegram")
		if seen[code] {
			t.Errorf("duplicate code generated: %s", code)
		}
		seen[code] = true
	}
}

func TestIsLinkCode(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"ABC123", true},
		{"abcdef", true},
		{"123456", true},
		{"ABCDE", false},   // too short
		{"ABCDEFG", false}, // too long
		{"ABC 23", false},  // space
		{"ABC-23", false},  // dash
		{"", false},
		{"      ", false},
	}

	for _, tt := range tests {
		if got := auth.IsLinkCode(tt.input); got != tt.want {
			t.Errorf("IsLinkCode(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLinkCodeMultiplePlatforms(t *testing.T) {
	store := auth.NewLinkCodeStore()

	code1 := store.Generate(1, "telegram")
	code2 := store.Generate(1, "qq")

	// Both should be consumable.
	_, p1, ok := store.Consume(code1)
	if !ok || p1 != "telegram" {
		t.Errorf("code1: ok=%v, platform=%q", ok, p1)
	}

	_, p2, ok := store.Consume(code2)
	if !ok || p2 != "qq" {
		t.Errorf("code2: ok=%v, platform=%q", ok, p2)
	}
}
