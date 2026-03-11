package ai

import "testing"

func TestEventTypes(t *testing.T) {
	tests := []struct {
		name string
		ev   AssistantEvent
		want string
	}{
		{"start", EventStart{}, "start"},
		{"text_start", EventTextStart{}, "text_start"},
		{"text_delta", EventTextDelta{}, "text_delta"},
		{"text_end", EventTextEnd{}, "text_end"},
		{"thinking_start", EventThinkingStart{}, "thinking_start"},
		{"thinking_delta", EventThinkingDelta{}, "thinking_delta"},
		{"thinking_end", EventThinkingEnd{}, "thinking_end"},
		{"toolcall_start", EventToolCallStart{}, "toolcall_start"},
		{"toolcall_delta", EventToolCallDelta{}, "toolcall_delta"},
		{"toolcall_end", EventToolCallEnd{}, "toolcall_end"},
		{"usage", EventUsage{}, "usage"},
		{"stop", EventStop{}, "stop"},
		{"error", EventError{}, "error"},
		{"done", EventDone{}, "done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ev.eventType(); got != tt.want {
				t.Errorf("eventType() = %q, want %q", got, tt.want)
			}
		})
	}
}
