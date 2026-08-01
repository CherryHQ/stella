package openai

import (
	"testing"
)

func TestNewProvider(t *testing.T) {
	p := New(Config{APIKey: "test-key"})
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewProviderWithBaseURL(t *testing.T) {
	p := New(Config{APIKey: "key", BaseURL: "https://example.com"})
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewProviderEmpty(t *testing.T) {
	p := New(Config{})
	if p == nil {
		t.Fatal("expected non-nil provider even with empty config")
	}
}

func TestProviderAPI(t *testing.T) {
	p := New(Config{})
	if p.API() != "openai" {
		t.Errorf("API() = %q, want %q", p.API(), "openai")
	}
}

func TestMapStopReasonValues(t *testing.T) {
	tests := []struct {
		reason string
		want   string
	}{
		{"stop", "stop"},
		{"length", "length"},
		{"tool_calls", "toolUse"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		got := mapStopReason(tt.reason)
		if string(got) != tt.want {
			t.Errorf("mapStopReason(%q) = %q, want %q", tt.reason, got, tt.want)
		}
	}
}
