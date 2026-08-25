package openairesponse

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/responses"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestNewProvider(t *testing.T) {
	p := New(Config{APIKey: "test-key"})
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewProviderWithBaseURL(t *testing.T) {
	p := New(Config{APIKey: "test-key", BaseURL: "https://example.com/v1"})
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
	if p.API() != "openai-response" {
		t.Errorf("API() = %q, want %q", p.API(), "openai-response")
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

func TestBuildRequestOptionsEmpty(t *testing.T) {
	opts := ai.StreamOptions{}
	reqOpts := buildRequestOptions(opts)
	if len(reqOpts) != 0 {
		t.Errorf("expected 0 request options, got %d", len(reqOpts))
	}
}

func TestBuildRequestOptionsWithHeaders(t *testing.T) {
	opts := ai.StreamOptions{
		Headers: map[string]string{"X-Custom": "value", "X-Other": "val2"},
	}
	reqOpts := buildRequestOptions(opts)
	if len(reqOpts) != 2 {
		t.Errorf("expected 2 request options, got %d", len(reqOpts))
	}
}

func TestLeadingNewlineStripperNoNewlines(t *testing.T) {
	s := &leadingNewlineStripper{rc: io.NopCloser(strings.NewReader("hello world"))}
	buf := make([]byte, 32)
	n, err := s.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Read error: %v", err)
	}
	if string(buf[:n]) != "hello world" {
		t.Errorf("got %q, want %q", string(buf[:n]), "hello world")
	}
}

func TestLeadingNewlineStripperWithNewlines(t *testing.T) {
	s := &leadingNewlineStripper{rc: io.NopCloser(strings.NewReader("\n\r\nhello"))}
	buf := make([]byte, 32)
	n, err := s.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Read error: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("got %q, want %q", string(buf[:n]), "hello")
	}
}

func TestLeadingNewlineStripperMultipleReads(t *testing.T) {
	s := &leadingNewlineStripper{rc: io.NopCloser(strings.NewReader("\nhello\nworld"))}
	// First read should strip leading newline
	buf := make([]byte, 5)
	n, _ := s.Read(buf)
	got := string(buf[:n])

	// Second read should pass through normally
	buf2 := make([]byte, 32)
	n2, _ := s.Read(buf2)
	got += string(buf2[:n2])

	if !strings.HasPrefix(got, "hello") {
		t.Errorf("got %q, want prefix 'hello'", got)
	}
}

func TestLeadingNewlineStripperClose(t *testing.T) {
	s := &leadingNewlineStripper{rc: io.NopCloser(strings.NewReader("test"))}
	if err := s.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}
}

func TestMapStopReason(t *testing.T) {
	tests := []struct {
		status responses.ResponseStatus
		want   ai.StopReason
	}{
		{responses.ResponseStatusCompleted, ai.StopReasonStop},
		{responses.ResponseStatusIncomplete, ai.StopReasonLength},
		{responses.ResponseStatusFailed, ai.StopReasonError},
		{"unknown_status", ai.StopReasonUnknown},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := mapStopReason(tt.status)
			if got != tt.want {
				t.Errorf("mapStopReason(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}
