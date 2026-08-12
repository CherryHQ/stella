package main

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
)

func TestModelMemoryToolAvailability(t *testing.T) {
	if got := modelMemoryToolAvailability(false); got != nil {
		t.Fatal("legacy mode unexpectedly restricts the model memory tool")
	}

	available := modelMemoryToolAvailability(true)
	if available == nil {
		t.Fatal("structured mode did not install a memory tool boundary")
	}
	if !available(context.Background(), agent.RunnerParams{UserID: "user-1", AgentID: "agent-1"}) {
		t.Fatal("structured mode hid the model memory tool from a DM")
	}
	if available(context.Background(), agent.RunnerParams{UserID: "group-1", GroupID: "group-1", AgentID: "agent-1"}) {
		t.Fatal("structured mode exposed the model memory tool to a group")
	}
}
