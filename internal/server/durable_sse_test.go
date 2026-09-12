package server

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
)

func TestLegacyIdleSSEWithoutEventStore(t *testing.T) {
	defer func() {
		if v := recover(); v != nil {
			t.Fatalf("flag-off idle SSE panicked instead of returning no live turn: %v", v)
		}
	}()
	s := &Server{}
	w := httptest.NewRecorder()
	ok, truncated := s.streamDurableTurn(t.Context(), w, w, "agent", "session", sessionaccess.AttachResult{}, "")
	if ok || truncated {
		t.Fatal("disabled durable mode must leave idle SSE on its existing 204 path")
	}
}

// The cursor is committed only after every frame of the durable event is
// flushed: the `id:` line must come after the event's delta, never before.
func TestCursorEmittedAfterEventFrames(t *testing.T) {
	ch := make(chan agent.Event, 1)
	ch <- agent.Event{Seq: 7, DurableID: "run-x:7", Text: "irreplaceable delta"}
	close(ch)
	w := httptest.NewRecorder()
	streamAgentEvents(t.Context(), w, w, "a", "s", ch, "", 0, nil)
	wire := w.Body.String()
	delta := strings.Index(wire, "irreplaceable delta")
	cursor := strings.Index(wire, "id: run-x:7")
	if delta < 0 || cursor < 0 {
		t.Fatalf("wire missing delta or cursor:\n%s", wire)
	}
	if cursor < delta {
		t.Fatal("cursor emitted before the event's delta — a disconnect would skip unsent frames")
	}
}

// streamWire is the parsed frame sequence of one SSE connection — the tests
// assert part ids and open/close ordering exactly as the AI-SDK parser sees
// them.
type streamWire struct {
	types []string
	ids   map[string][]string // frame type → part/tool ids, in order
	text  string              // concatenated text/reasoning deltas
}

func parseStreamWire(t *testing.T, body string) streamWire {
	t.Helper()
	w := streamWire{ids: map[string][]string{}}
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatalf("bad frame %q: %v", line, err)
		}
		typ, _ := frame["type"].(string)
		w.types = append(w.types, typ)
		if id, ok := frame["id"].(string); ok {
			w.ids[typ] = append(w.ids[typ], id)
		}
		if d, ok := frame["delta"].(string); ok {
			w.text += d
		}
	}
	return w
}

// requireStartsBeforeDeltas asserts every delta/end frame names a part this
// connection actually opened — the invariant the AI-SDK parser depends on.
func requireStartsBeforeDeltas(t *testing.T, w streamWire) {
	t.Helper()
	open := map[string]bool{}
	for i, typ := range w.types {
		switch typ {
		case "text-start", "reasoning-start":
			open[idFor(t, w, typ, i)] = true
		case "text-delta", "reasoning-delta", "text-end", "reasoning-end":
			id := idFor(t, w, typ, i)
			if !open[id] {
				t.Fatalf("frame %d (%s id=%s) has no preceding start on this connection; wire types %v", i, typ, id, w.types)
			}
			if typ == "text-end" || typ == "reasoning-end" {
				open[id] = false
			}
		}
	}
}

func countPrior(w streamWire, typ string, upto int) int {
	n := 0
	for i := range upto {
		if w.types[i] == typ {
			n++
		}
	}
	return n
}

func idFor(t *testing.T, w streamWire, typ string, i int) string {
	t.Helper()
	n := countPrior(w, typ, i)
	if n >= len(w.ids[typ]) {
		t.Fatalf("frame %d type %s missing id", i, typ)
	}
	return w.ids[typ][n]
}

func streamTurn(t *testing.T, events []agent.Event, scope string, resumeSeq int64) streamWire {
	t.Helper()
	ch := make(chan agent.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	w := httptest.NewRecorder()
	streamAgentEvents(t.Context(), w, w, "a", "s", ch, scope, resumeSeq, nil)
	return parseStreamWire(t, w.Body.String())
}

func seqEvents(start int64, evs ...agent.Event) []agent.Event {
	out := make([]agent.Event, len(evs))
	for i, e := range evs {
		e.Seq = start + int64(i)
		e.DurableID = "r1:" + strconv.FormatInt(e.Seq, 10)
		out[i] = e
	}
	return out
}

func TestResumeSuppressesConsumedText(t *testing.T) {
	events := seqEvents(1,
		agent.Event{Text: "ALPHA "},
		agent.Event{Text: "BETA"},
	)
	resumed := streamTurn(t, events, "r1", 1)
	if resumed.text != "BETA" {
		t.Fatalf("resumed stream re-sent consumed text: %q", resumed.text)
	}
	// The part re-opens under the same id as the uninterrupted stream.
	full := streamTurn(t, events, "r1", 0)
	if resumed.ids["text-start"][0] != full.ids["text-start"][0] {
		t.Fatalf("resumed part id %q != uninterrupted %q", resumed.ids["text-start"][0], full.ids["text-start"][0])
	}
	requireStartsBeforeDeltas(t, resumed)
}

func TestResumeAtToolBoundaryOpensNoPhantomPart(t *testing.T) {
	events := seqEvents(1,
		agent.Event{Text: "consumed"},
		agent.Event{ToolUse: &agent.ToolUseEvent{ID: "c1", Tool: "task", Status: "running"}},
		agent.Event{ToolUse: &agent.ToolUseEvent{ID: "c1", Tool: "task", Status: "done"}},
		agent.Event{Text: "after"},
	)
	resumed := streamTurn(t, events, "r1", 3)
	if resumed.text != "after" {
		t.Fatalf("unexpected re-sent text: %q", resumed.text)
	}
	if len(resumed.ids["text-start"]) != 1 {
		t.Fatalf("expected exactly one text part on resumed stream, got %v", resumed.ids["text-start"])
	}
	requireStartsBeforeDeltas(t, resumed)
	full := streamTurn(t, events, "r1", 0)
	if resumed.ids["text-start"][0] != full.ids["text-start"][1] {
		t.Fatalf("post-boundary part id %q diverged from uninterrupted stream's %q", resumed.ids["text-start"][0], full.ids["text-start"][1])
	}
}

func TestResumeLongPartKeepsFirstSeqID(t *testing.T) {
	var evs []agent.Event
	for range 600 {
		evs = append(evs, agent.Event{Text: "x"})
	}
	events := seqEvents(1, evs...)
	resumed := streamTurn(t, events, "r1", 550)
	if want := "r1:t:1"; resumed.ids["text-start"][0] != want {
		t.Fatalf("long part resumed under %q, want %q — the part id must derive from its first seq, not the window", resumed.ids["text-start"][0], want)
	}
	if len(resumed.text) != 50 {
		t.Fatalf("resumed deltas = %d, want the 50 unconsumed ones", len(resumed.text))
	}
	requireStartsBeforeDeltas(t, resumed)
}

func TestResumeReasoningToTextTransition(t *testing.T) {
	events := seqEvents(1,
		agent.Event{Reasoning: "thinking"},
		agent.Event{Reasoning: " more"},
		agent.Event{Text: "answer"},
	)
	resumed := streamTurn(t, events, "r1", 2)
	// The primed reasoning part opens then closes on this connection before
	// the text part starts.
	want := []string{"reasoning-start", "reasoning-end", "text-start", "text-delta"}
	var got []string
	for _, typ := range resumed.types {
		if typ == "reasoning-start" || typ == "reasoning-end" || typ == "text-start" || typ == "text-delta" {
			got = append(got, typ)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("frame order %v, want %v", got, want)
	}
	requireStartsBeforeDeltas(t, resumed)
}

func TestResumeFullyConsumedWritesNoBareEnds(t *testing.T) {
	events := seqEvents(1,
		agent.Event{Text: "ALPHA"},
		agent.Event{Step: &agent.StepEvent{Kind: "finish"}},
	)
	resumed := streamTurn(t, events, "r1", 2)
	for _, typ := range resumed.types {
		switch typ {
		case "text-end", "reasoning-end", "finish-step":
			t.Fatalf("resume emitted a bare %s for a part this connection never opened: %v", typ, resumed.types)
		}
	}

	// The harder case: the primed part is still OPEN at the cursor — the
	// connection emitted no start for it, so EOF must not write its end.
	openPart := streamTurn(t, seqEvents(1, agent.Event{Text: "ALPHA"}), "r1", 1)
	for _, typ := range openPart.types {
		if typ == "text-end" {
			t.Fatalf("resume emitted text-end for a part this connection never opened: %v", openPart.types)
		}
	}
}
