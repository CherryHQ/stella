package main

import (
	"encoding/json"
	"testing"
)

// messageFixture is written as JSON so the test exercises the same decoding the
// driver performs against the sessions API.
const messageFixture = `[
  {"role":"user","token_count":40,"timestamp":"2026-08-19T10:00:00Z"},
  {"role":"assistant","token_count":100,"timestamp":"2026-08-19T10:00:02Z",
   "blocks":[{"type":"text","text":"looking"},{"type":"tool_call","id":"c1","name":"bash","arguments":{"command":"pwd"}}]},
  {"role":"tool","token_count":10,"timestamp":"2026-08-19T10:00:03Z","tool_call_id":"c1"},
  {"role":"assistant","token_count":150,"timestamp":"2026-08-19T10:00:09Z",
   "blocks":[{"type":"tool_call","id":"c2","name":"write","arguments":{"path":"/app/x"}}]},
  {"role":"tool","token_count":5,"timestamp":"2026-08-19T10:00:13Z","tool_call_id":"c2","is_error":true},
  {"role":"assistant","token_count":20,"timestamp":"2026-08-19T10:00:14Z","blocks":[{"type":"text","text":"done"}]}
]`

func TestDeriveMetricsAttributesTurnsToolsAndTime(t *testing.T) {
	var messages []sessionMessage
	if err := json.Unmarshal([]byte(messageFixture), &messages); err != nil {
		t.Fatal(err)
	}

	m, calls := deriveMetrics(messages)

	if m.Turns != 3 {
		t.Errorf("turns = %d, want 3", m.Turns)
	}
	if m.ToolCallTotal != 2 || len(calls) != 2 {
		t.Errorf("tool calls = %d (recorded %d), want 2", m.ToolCallTotal, len(calls))
	}
	if m.ToolErrorTotal != 1 || m.Tools["write"].Errors != 1 {
		t.Errorf("tool errors not attributed: %+v", m.Tools)
	}
	if got := m.Tools["bash"]; got.Calls != 1 || got.TotalMs != 1000 || got.MaxMs != 1000 {
		t.Errorf("bash stat = %+v, want 1 call of 1000ms", got)
	}
	if got := m.Tools["write"]; got.TotalMs != 4000 {
		t.Errorf("write total = %dms, want 4000", got.TotalMs)
	}
	if m.SlowestToolCall == nil || m.SlowestToolCall.Name != "write" || m.SlowestToolCall.Ms != 4000 {
		t.Errorf("slowest tool call = %+v, want write 4000ms", m.SlowestToolCall)
	}
	// 2s to the first reply, 6s and 1s before the later replies.
	if m.Timing.ModelMs != 9000 {
		t.Errorf("model time = %dms, want 9000", m.Timing.ModelMs)
	}
	if m.Timing.ToolMs != 5000 {
		t.Errorf("tool time = %dms, want 5000", m.Timing.ToolMs)
	}
	if m.Timing.FirstResponseMs != 2000 {
		t.Errorf("first response = %dms, want 2000", m.Timing.FirstResponseMs)
	}
	if m.Tokens.Total != 325 || m.Tokens.Assistant != 270 || m.Tokens.Tool != 15 || m.Tokens.User != 40 {
		t.Errorf("token breakdown = %+v", m.Tokens)
	}
}

// Messages committed in one transaction can share or invert a timestamp; a
// negative duration in a report is worse than a zero.
func TestDeriveMetricsNeverReportsNegativeDurations(t *testing.T) {
	var messages []sessionMessage
	if err := json.Unmarshal([]byte(`[
	  {"role":"assistant","timestamp":"2026-08-19T10:00:05Z","blocks":[{"type":"tool_call","id":"c1","name":"bash"}]},
	  {"role":"tool","timestamp":"2026-08-19T10:00:04Z","tool_call_id":"c1"},
	  {"role":"assistant","timestamp":"2026-08-19T10:00:03Z"}
	]`), &messages); err != nil {
		t.Fatal(err)
	}

	m, _ := deriveMetrics(messages)

	if m.Timing.ToolMs != 0 || m.Timing.ModelMs != 0 {
		t.Errorf("out-of-order timestamps produced %+v", m.Timing)
	}
}

// A turn that never reached a tool result must not silently drop the call from
// the counts; only its duration is unknown.
func TestDeriveMetricsCountsAToolCallWithoutAResult(t *testing.T) {
	var messages []sessionMessage
	if err := json.Unmarshal([]byte(`[
	  {"role":"user","timestamp":"2026-08-19T10:00:00Z"},
	  {"role":"assistant","timestamp":"2026-08-19T10:00:01Z","blocks":[{"type":"tool_call","id":"c1","name":"bash"}]}
	]`), &messages); err != nil {
		t.Fatal(err)
	}

	m, calls := deriveMetrics(messages)

	if m.ToolCallTotal != 1 || len(calls) != 1 || m.Tools["bash"].Calls != 1 {
		t.Errorf("unfinished tool call was dropped: %+v", m)
	}
	if m.Tools["bash"].TotalMs != 0 || m.Timing.ToolMs != 0 {
		t.Errorf("unfinished tool call must contribute no duration: %+v", m)
	}
}
