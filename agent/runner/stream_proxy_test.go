package runner

import (
	"testing"

	aitypes "github.com/vaayne/anna/ai/types"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	raw, err := EncodeEvent(aitypes.EventTextDelta{Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected encode error: %v", err)
	}
	event, err := DecodeEvent(raw)
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	text, ok := event.(aitypes.EventTextDelta)
	if !ok || text.Text != "hello" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestEncodeDecodeStop(t *testing.T) {
	raw, err := EncodeEvent(aitypes.EventStop{Reason: aitypes.StopReasonStop})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	event, err := DecodeEvent(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	stop, ok := event.(aitypes.EventStop)
	if !ok || stop.Reason != aitypes.StopReasonStop {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestEncodeDecodeUsage(t *testing.T) {
	raw, err := EncodeEvent(aitypes.EventUsage{Usage: aitypes.Usage{InputTokens: 10, OutputTokens: 20}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	event, err := DecodeEvent(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	usage, ok := event.(aitypes.EventUsage)
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
	raw, err := EncodeEvent(aitypes.EventStart{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// EventStart maps to "unknown", which DecodeEvent doesn't support
	_, err = DecodeEvent(raw)
	if err == nil {
		t.Fatal("expected error for unknown event type")
	}
}
