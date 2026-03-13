package ai

import (
	"errors"
	"testing"
)

// mockProvider implements ProviderAdapter for testing.
type mockProvider struct {
	api string
}

func (m *mockProvider) API() string { return m.api }
func (m *mockProvider) Stream(Model, Context, StreamOptions) (AssistantEventStream, error) {
	return nil, nil
}
func (m *mockProvider) StreamSimple(Model, Context, SimpleStreamOptions) (AssistantEventStream, error) {
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
	_, err := Stream(Model{API: "missing"}, Context{}, StreamOptions{}, r)
	if !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("expected ErrProviderNotFound, got %v", err)
	}
}

func TestStreamSimpleProviderNotFound(t *testing.T) {
	r := NewRegistry()
	_, err := StreamSimple(Model{API: "missing"}, Context{}, SimpleStreamOptions{}, r)
	if !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("expected ErrProviderNotFound, got %v", err)
	}
}

func TestStreamDispatchesToProvider(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockProvider{api: "test"})
	stream, err := Stream(Model{API: "test"}, Context{}, StreamOptions{}, r)
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
	stream, err := StreamSimple(Model{API: "test"}, Context{}, SimpleStreamOptions{}, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream != nil {
		t.Error("expected nil stream from mock")
	}
}

func TestChannelEventStream(t *testing.T) {
	s := NewChannelEventStream(8)

	s.Emit(EventTextDelta{Text: "hello"})
	s.Emit(EventStop{Reason: StopReasonStop})
	s.Finish(nil)

	var events []AssistantEvent
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
	_, err := Complete(Model{API: "missing"}, Context{}, CompleteOptions{}, r)
	if !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("expected ErrProviderNotFound, got %v", err)
	}
}

func TestCompleteAssemblesMessage(t *testing.T) {
	s := NewChannelEventStream(16)
	go func() {
		s.Emit(EventTextDelta{Text: "hello "})
		s.Emit(EventTextDelta{Text: "world"})
		s.Emit(EventThinkingDelta{Thinking: "hmm"})
		s.Emit(EventToolCallDelta{ID: "t1", Name: "search", Arguments: `{"q":"test"}`})
		s.Emit(EventUsage{Usage: Usage{InputTokens: 10, OutputTokens: 5}})
		s.Emit(EventStop{Reason: StopReasonStop})
		s.Finish(nil)
	}()

	r := NewRegistry()
	r.Register(&streamMockProvider{api: "test", stream: s})

	msg, err := Complete(Model{API: "test"}, Context{}, CompleteOptions{}, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.StopReason != StopReasonStop {
		t.Errorf("StopReason = %q, want %q", msg.StopReason, StopReasonStop)
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
		s.Emit(EventError{Err: errors.New("upstream error")})
		s.Finish(nil)
	}()

	r := NewRegistry()
	r.Register(&streamMockProvider{api: "test", stream: s})

	msg, err := Complete(Model{API: "test"}, Context{}, CompleteOptions{}, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.StopReason != StopReasonError {
		t.Errorf("StopReason = %q, want %q", msg.StopReason, StopReasonError)
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
func (m *streamMockProvider) Stream(Model, Context, StreamOptions) (AssistantEventStream, error) {
	return m.stream, nil
}
func (m *streamMockProvider) StreamSimple(Model, Context, SimpleStreamOptions) (AssistantEventStream, error) {
	return m.stream, nil
}
