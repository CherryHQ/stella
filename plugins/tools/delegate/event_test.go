package delegate

import (
	"testing"
	"time"
)

func TestDelegateStarted_Kind(t *testing.T) {
	ev := DelegateStarted{TaskID: "t1", Preset: "worker"}
	if ev.Kind() != "delegateStarted" {
		t.Errorf("expected 'delegateStarted', got %q", ev.Kind())
	}
}

func TestDelegateFinished_Kind(t *testing.T) {
	ev := DelegateFinished{TaskID: "t1", Duration: time.Second}
	if ev.Kind() != "delegateFinished" {
		t.Errorf("expected 'delegateFinished', got %q", ev.Kind())
	}
}
