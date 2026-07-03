package providers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestAdapterStreamFunc(t *testing.T) {
	p := &mockProvider{api: "test"}
	sf := AdapterStreamFunc(p)
	stream, err := sf(context.Background(), ai.Model{}, ai.Context{}, ai.StreamOptions{})
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

	for range s.Events() {
	}

	if got := s.Wait(); !errors.Is(got, want) {
		t.Errorf("Wait() = %v, want %v", got, want)
	}
}

func TestChannelEventStreamClose(t *testing.T) {
	s := NewChannelEventStream(1)
	if err := s.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}

	if err := s.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
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

	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (AssistantEventStream, error) {
		return s, nil
	}

	msg, err := Complete(context.Background(), ai.Model{}, ai.Context{}, ai.CompleteOptions{}, stream)
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

func TestCompletePreservesToolCallOrder(t *testing.T) {
	wantIDs := []string{
		"call-00",
		"call-01",
		"call-02",
		"call-03",
		"call-04",
		"call-05",
		"call-06",
		"call-07",
	}

	for attempt := range 20 {
		stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (AssistantEventStream, error) {
			s := NewChannelEventStream(16)
			for i, id := range wantIDs {
				s.Emit(ai.EventToolCallDelta{
					ID:        id,
					Name:      fmt.Sprintf("tool_%02d", i),
					Arguments: fmt.Sprintf(`{"i":%d}`, i),
				})
			}
			s.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			s.Finish(nil)
			return s, nil
		}

		msg, err := Complete(context.Background(), ai.Model{}, ai.Context{}, ai.CompleteOptions{}, stream)
		if err != nil {
			t.Fatalf("attempt %d: unexpected error: %v", attempt, err)
		}

		gotIDs := make([]string, 0, len(wantIDs))
		for _, block := range msg.Content {
			call, ok := block.(ai.ToolCall)
			if !ok {
				continue
			}
			gotIDs = append(gotIDs, call.ID)
		}
		if len(gotIDs) != len(wantIDs) {
			t.Fatalf("attempt %d: got %d tool calls, want %d: %#v", attempt, len(gotIDs), len(wantIDs), gotIDs)
		}
		for i := range wantIDs {
			if gotIDs[i] != wantIDs[i] {
				t.Fatalf("attempt %d: tool call order = %#v, want %#v", attempt, gotIDs, wantIDs)
			}
		}
	}
}

func TestCompleteWithErrorEvent(t *testing.T) {
	s := NewChannelEventStream(4)
	go func() {
		s.Emit(ai.EventError{Err: errors.New("upstream error")})
		s.Finish(nil)
	}()

	stream := func(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (AssistantEventStream, error) {
		return s, nil
	}

	msg, err := Complete(context.Background(), ai.Model{}, ai.Context{}, ai.CompleteOptions{}, stream)
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

// mockProvider implements ProviderAdapter for testing.
type mockProvider struct {
	api string
}

func (m *mockProvider) API() string { return m.api }
func (m *mockProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (AssistantEventStream, error) {
	return nil, nil
}
