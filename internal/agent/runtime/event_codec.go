package runtime

import (
	"encoding/json"
	"errors"

	"github.com/CherryHQ/stella/pkg/renderrefs"
)

// EncodeEvent serializes the durable subset of an Event for
// ctx_session_event. Transport-internal fields are dropped by contract:
// Store events are already durable in session history, and Err is persisted
// as text. The decoder produces a watcher-safe Event.
func EncodeEvent(ev Event) json.RawMessage {
	enc := encodedEvent{
		Text:       ev.Text,
		Reasoning:  ev.Reasoning,
		Image:      ev.Image,
		File:       ev.File,
		ToolUse:    ev.ToolUse,
		References: ev.References,
		Step:       ev.Step,
	}
	if ev.Err != nil {
		enc.Err = ev.Err.Error()
	}
	raw, err := json.Marshal(enc)
	if err != nil {
		// A malformed event must not wedge the turn: persist an error marker
		// instead so the watcher stream stays contiguous.
		raw, _ = json.Marshal(encodedEvent{Err: "event encode failed"})
	}
	return raw
}

// DecodeEvent reconstructs the stored event. The Store field can never come
// back from the log — history carries it — and Err becomes a plain error.
func DecodeEvent(raw json.RawMessage) (Event, error) {
	var enc encodedEvent
	if err := json.Unmarshal(raw, &enc); err != nil {
		return Event{}, err
	}
	ev := Event{
		Text:       enc.Text,
		Reasoning:  enc.Reasoning,
		Image:      enc.Image,
		File:       enc.File,
		ToolUse:    enc.ToolUse,
		References: enc.References,
		Step:       enc.Step,
	}
	if enc.Err != "" {
		ev.Err = errors.New(enc.Err)
	}
	return ev, nil
}

type encodedEvent struct {
	Text       string                 `json:"text,omitempty"`
	Reasoning  string                 `json:"reasoning,omitempty"`
	Image      *ImageEvent            `json:"image,omitempty"`
	File       *FileEvent             `json:"file,omitempty"`
	ToolUse    *ToolUseEvent          `json:"tool_use,omitempty"`
	References []renderrefs.Reference `json:"references,omitempty"`
	Step       *StepEvent             `json:"step,omitempty"`
	Err        string                 `json:"err,omitempty"`
}
