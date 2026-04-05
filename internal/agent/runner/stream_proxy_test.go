package runner

import (
	"testing"

	"github.com/vaayne/anna/pkg/ai"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	raw, err := EncodeEvent(ai.EventTextDelta{Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected encode error: %v", err)
	}
	event, err := DecodeEvent(raw)
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	text, ok := event.(ai.EventTextDelta)
	if !ok || text.Text != "hello" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestEncodeDecodeStop(t *testing.T) {
	raw, err := EncodeEvent(ai.EventStop{Reason: ai.StopReasonStop})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	event, err := DecodeEvent(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	stop, ok := event.(ai.EventStop)
	if !ok || stop.Reason != ai.StopReasonStop {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestEncodeDecodeUsage(t *testing.T) {
	raw, err := EncodeEvent(ai.EventUsage{Usage: ai.Usage{InputTokens: 10, OutputTokens: 20}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	event, err := DecodeEvent(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	usage, ok := event.(ai.EventUsage)
	if !ok || usage.Usage.InputTokens != 10 {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestDecodeEventUnknownType(t *testing.T) {
	raw := []byte(`{"type":"foo","data":{}}`)
	_, err := DecodeEvent(raw)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestDecodeEventBadJSON(t *testing.T) {
	_, err := DecodeEvent([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
}

func TestEventNameUnknown(t *testing.T) {
	raw, err := EncodeEvent(ai.EventStart{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// EventStart maps to "unknown", which DecodeEvent doesn't support
	_, err = DecodeEvent(raw)
	if err == nil {
		t.Fatal("expected error for unknown event type")
	}
}
