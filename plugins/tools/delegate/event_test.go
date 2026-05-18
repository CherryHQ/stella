package delegate

import (
	"testing"
	"time"
)

func TestSubDelegateStarted_Kind(t *testing.T) {
	ev := SubDelegateStarted{TaskID: "t1", Preset: "worker"}
	if ev.Kind() != "subDelegateStarted" {
		t.Errorf("expected 'subAgentStarted', got %q", ev.Kind())
	}
}

func TestSubDelegateFinished_Kind(t *testing.T) {
	ev := SubDelegateFinished{TaskID: "t1", Duration: time.Second}
	if ev.Kind() != "subDelegateFinished" {
		t.Errorf("expected 'subAgentFinished', got %q", ev.Kind())
	}
}
