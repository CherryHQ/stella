package agent

import (
	"testing"
	"time"
)

func TestSubAgentStarted_Kind(t *testing.T) {
	ev := SubAgentStarted{TaskID: "t1", Preset: "worker"}
	if ev.Kind() != "subAgentStarted" {
		t.Errorf("expected 'subAgentStarted', got %q", ev.Kind())
	}
}

func TestSubAgentFinished_Kind(t *testing.T) {
	ev := SubAgentFinished{TaskID: "t1", Duration: time.Second}
	if ev.Kind() != "subAgentFinished" {
		t.Errorf("expected 'subAgentFinished', got %q", ev.Kind())
	}
}
