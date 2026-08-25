package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
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

func TestProviderDoesNotRetryOutcomeUnknownModelRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		delay  time.Duration
	}{
		{"rate limit", http.StatusTooManyRequests, 0},
		{"server error", http.StatusServiceUnavailable, 0},
		{"timeout", http.StatusOK, 100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				if tc.delay > 0 {
					time.Sleep(tc.delay)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			ctx := t.Context()
			if tc.delay > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			stream, err := New(Config{APIKey: "test", BaseURL: server.URL + "/v1"}).Stream(ctx,
				ai.Model{Name: "model"}, ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "hello"}}}, ai.StreamOptions{})
			failed := err != nil
			if err == nil {
				for event := range stream.Events() {
					if _, ok := event.(ai.EventError); ok {
						failed = true
					}
				}
			}
			if !failed {
				t.Fatal("failed model request returned no error")
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("model requests = %d, want exactly one", got)
			}
		})
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
