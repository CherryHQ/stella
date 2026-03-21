package feishutool

import (
	"strings"
	"testing"
)

func TestParseTimeToUnix(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"2024-01-15T10:30:00Z", 1705314600},
		{"2024-01-15 10:30:00", 1705314600},
		{"2024-01-15", 1705276800},
		{"1705314600", 1705314600},
	}

	for _, tt := range tests {
		got, err := ParseTimeToUnix(tt.input)
		if err != nil {
			t.Errorf("ParseTimeToUnix(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseTimeToUnix(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseTimeToUnixError(t *testing.T) {
	_, err := ParseTimeToUnix("not-a-time")
	if err == nil {
		t.Fatal("expected error for invalid time string")
	}
}

func TestParseTimeToUnixMs(t *testing.T) {
	got, err := ParseTimeToUnixMs("2024-01-15T10:30:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1705314600000 {
		t.Fatalf("got %d, want 1705314600000", got)
	}
}

func TestFormatLarkError(t *testing.T) {
	got := FormatLarkError(99991, "permission denied")
	if !strings.Contains(got, "99991") || !strings.Contains(got, "permission denied") {
		t.Fatalf("unexpected error format: %s", got)
	}
}

func TestJSONResult(t *testing.T) {
	result, err := JSONResult(map[string]any{"name": "alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "alice") {
		t.Fatalf("result should contain alice: %s", result)
	}
}

func TestJSONResultFromAny(t *testing.T) {
	result, err := JSONResultFromAny([]string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "a") || !strings.Contains(result, "b") {
		t.Fatalf("result should contain a and b: %s", result)
	}
}

func TestStringArg(t *testing.T) {
	args := map[string]any{"key": "value"}
	if got := stringArg(args, "key"); got != "value" {
		t.Fatalf("expected value, got %q", got)
	}
	if got := stringArg(args, "missing"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestDerefStr(t *testing.T) {
	if got := derefStr(nil); got != "" {
		t.Fatalf("expected empty for nil, got %q", got)
	}
	s := "hello"
	if got := derefStr(&s); got != "hello" {
		t.Fatalf("expected hello, got %q", got)
	}
}

func TestDerefInt(t *testing.T) {
	if got := derefInt(nil); got != 0 {
		t.Fatalf("expected 0 for nil, got %d", got)
	}
	n := 42
	if got := derefInt(&n); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
}
