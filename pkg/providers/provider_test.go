package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/vaayne/anna/pkg/ai"
)

// mockProvider implements ProviderAdapter for testing.
type mockProvider struct {
	api string
}

func (m *mockProvider) API() string { return m.api }
func (m *mockProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (AssistantEventStream, error) {
	return nil, nil
}
func (m *mockProvider) StreamSimple(context.Context, ai.Model, ai.Context, ai.SimpleStreamOptions) (AssistantEventStream, error) {
	return nil, nil
}

func TestDefaultRegistry(t *testing.T) {
	r := DefaultRegistry()
	if r == nil {
		t.Fatal("DefaultRegistry() returned nil")
	}
	// Should return the same instance
	r2 := DefaultRegistry()
	if r != r2 {
		t.Error("DefaultRegistry() should return singleton")
	}
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	p := &mockProvider{api: "test-api"}
	r.Register(p)

	got, ok := r.Get("test-api")
	if !ok {
		t.Fatal("expected provider to be found")
	}
	if got.API() != "test-api" {
		t.Errorf("API() = %q, want %q", got.API(), "test-api")
	}
}

func TestRegistryGetMissing(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get("nonexistent")
	if ok {
		t.Error("expected provider not found")
	}
}

func TestStreamProviderNotFound(t *testing.T) {
	r := NewRegistry()
	_, err := Stream(context.Background(), ai.Model{API: "missing"}, ai.Context{}, ai.StreamOptions{}, r)
	if !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("expected ErrProviderNotFound, got %v", err)
	}
}

func TestStreamSimpleProviderNotFound(t *testing.T) {
	r := NewRegistry()
	_, err := StreamSimple(context.Background(), ai.Model{API: "missing"}, ai.Context{}, ai.SimpleStreamOptions{}, r)
	if !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("expected ErrProviderNotFound, got %v", err)
	}
}

func TestStreamDispatchesToProvider(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockProvider{api: "test"})
	stream, err := Stream(context.Background(), ai.Model{API: "test"}, ai.Context{}, ai.StreamOptions{}, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream != nil {
		t.Error("expected nil stream from mock")
	}
}

func TestStreamSimpleDispatchesToProvider(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockProvider{api: "test"})
	stream, err := StreamSimple(context.Background(), ai.Model{API: "test"}, ai.Context{}, ai.SimpleStreamOptions{}, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream != nil {
		t.Error("expected nil stream from mock")
	}
}

func TestChannelEventStream(t *testing.T) {
	s := NewChannelEventStream(8)

	s.Emit(ai.EventTextDelta{Text: "hello"})
	s.Emit(ai.EventStop{Reason: ai.StopReasonStop})
	s.Finish(nil)

	var events []ai.AssistantEvent
	for e := range s.Events() {
		events = append(events, e)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if err := s.Wait(); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestChannelEventStreamFinishWithError(t *testing.T) {
	s := NewChannelEventStream(1)
	want := errors.New("stream failed")
	s.Finish(want)

	// Drain events channel
	for range s.Events() {
	}

	if got := s.Wait(); got != want {
		t.Errorf("Wait() = %v, want %v", got, want)
	}
}

func TestChannelEventStreamClose(t *testing.T) {
	s := NewChannelEventStream(1)
	if err := s.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}

	// Double close should not panic
	if err := s.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
}

func TestCompleteProviderNotFound(t *testing.T) {
	r := NewRegistry()
	_, err := Complete(context.Background(), ai.Model{API: "missing"}, ai.Context{}, ai.CompleteOptions{}, r)
	if !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("expected ErrProviderNotFound, got %v", err)
	}
}

func TestCompleteAssemblesMessage(t *testing.T) {
	s := NewChannelEventStream(16)
	go func() {
		s.Emit(ai.EventTextDelta{Text: "hello "})
		s.Emit(ai.EventTextDelta{Text: "world"})
		s.Emit(ai.EventThinkingDelta{Thinking: "hmm"})
		s.Emit(ai.EventToolCallDelta{ID: "t1", Name: "search", Arguments: `{"q":"test"}`})
		s.Emit(ai.EventUsage{Usage: ai.Usage{InputTokens: 10, OutputTokens: 5}})
		s.Emit(ai.EventStop{Reason: ai.StopReasonStop})
		s.Finish(nil)
	}()

	r := NewRegistry()
	r.Register(&streamMockProvider{api: "test", stream: s})

	msg, err := Complete(context.Background(), ai.Model{API: "test"}, ai.Context{}, ai.CompleteOptions{}, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.StopReason != ai.StopReasonStop {
		t.Errorf("StopReason = %q, want %q", msg.StopReason, ai.StopReasonStop)
	}
	if msg.Usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", msg.Usage.InputTokens)
	}
	if len(msg.Content) != 3 {
		t.Errorf("expected 3 content blocks, got %d", len(msg.Content))
	}
}

func TestCompleteWithErrorEvent(t *testing.T) {
	s := NewChannelEventStream(4)
	go func() {
		s.Emit(ai.EventError{Err: errors.New("upstream error")})
		s.Finish(nil)
	}()

	r := NewRegistry()
	r.Register(&streamMockProvider{api: "test", stream: s})

	msg, err := Complete(context.Background(), ai.Model{API: "test"}, ai.Context{}, ai.CompleteOptions{}, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.StopReason != ai.StopReasonError {
		t.Errorf("StopReason = %q, want %q", msg.StopReason, ai.StopReasonError)
	}
	if msg.ErrorMessage == "" {
		t.Error("expected non-empty ErrorMessage")
	}
}

// streamMockProvider returns a pre-built stream.
type streamMockProvider struct {
	api    string
	stream AssistantEventStream
}

func (m *streamMockProvider) API() string { return m.api }
func (m *streamMockProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (AssistantEventStream, error) {
	return m.stream, nil
}
func (m *streamMockProvider) StreamSimple(context.Context, ai.Model, ai.Context, ai.SimpleStreamOptions) (AssistantEventStream, error) {
	return m.stream, nil
}
