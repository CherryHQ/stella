package openai

import (
	"io"
	"testing"
	"time"
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

func TestSSECommentStripperHandlesFragmentedLeadingComment(t *testing.T) {
	source := &chunkReader{chunks: [][]byte{
		[]byte(": keep"),
		[]byte("-alive\r"),
		[]byte("\n\r\n"),
		[]byte("data: {\"ok\":true}\n\n"),
	}}
	stripper := &sseCommentStripper{rc: io.NopCloser(source)}

	type readResult struct {
		data []byte
		err  error
	}
	result := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(stripper)
		result <- readResult{data: data, err: err}
	}()

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("read stripped SSE stream: %v", got.err)
		}
		if want := "data: {\"ok\":true}\n\n"; string(got.data) != want {
			t.Fatalf("stripped SSE stream = %q, want %q", got.data, want)
		}
	case <-time.After(time.Second):
		t.Fatal("fragmented leading SSE comment caused Read to stall")
	}
}

type chunkReader struct {
	chunks [][]byte
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	return n, nil
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
