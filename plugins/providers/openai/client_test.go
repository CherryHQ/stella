package openai

import (
	"io"
	"strings"
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
		{"unknown", "stop"},
	}
	for _, tt := range tests {
		got := mapStopReason(tt.reason)
		if string(got) != tt.want {
			t.Errorf("mapStopReason(%q) = %q, want %q", tt.reason, got, tt.want)
		}
	}
}

func TestSSECommentStripperPassesThroughData(t *testing.T) {
	t.Parallel()

	input := "data: {\"message\":\"ok\"}\n\ndata: [DONE]\n\n"
	reader := &sseCommentStripper{rc: io.NopCloser(strings.NewReader(input))}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != input {
		t.Fatalf("ReadAll() = %q, want %q", got, input)
	}
}

func TestSSECommentStripperRemovesLeadingComments(t *testing.T) {
	t.Parallel()

	longComment := ": " + strings.Repeat("x", 700) + "\n\n"
	input := ": keep-alive\n\n" + longComment + "data: {\"message\":\"ok\"}\n\n"
	want := "data: {\"message\":\"ok\"}\n\n"
	reader := &sseCommentStripper{rc: io.NopCloser(strings.NewReader(input))}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("ReadAll() = %q, want %q", got, want)
	}
}
