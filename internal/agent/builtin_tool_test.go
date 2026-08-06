package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/pkg/tools"
)

type namedTool string

func (n namedTool) Definition() tools.Definition                            { return tools.Definition{Name: string(n)} }
func (n namedTool) Execute(context.Context, map[string]any) (string, error) { return "{}", nil }

func TestBuiltinToolAvailable(t *testing.T) {
	tests := []struct {
		name string
		p    RunnerParams
		want bool
	}{
		{name: "user and agent", p: RunnerParams{UserID: "u1", AgentID: "a1"}, want: true},
		{name: "group no user", p: RunnerParams{AgentID: "a1", GroupID: "g1"}, want: false},
		{name: "no agent", p: RunnerParams{UserID: "u1"}, want: false},
		{name: "goal worker still has user and agent", p: RunnerParams{UserID: "u1", AgentID: "a1", ExtraTools: []tools.Tool{namedTool("goal_control")}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuiltinToolAvailable(context.Background(), tt.p); got != tt.want {
				t.Fatalf("BuiltinToolAvailable=%v want %v", got, tt.want)
			}
		})
	}
}

func TestGroupMemoryBuiltinAvailability(t *testing.T) {
	group := RunnerParams{UserID: "synthetic-group-id", GroupID: "group-1", AgentID: "agent-1"}
	if NonGroupBuiltinToolAvailable(context.Background(), group) {
		t.Fatal("private memory tool must be unavailable in a group runner")
	}
	if !GroupBuiltinToolAvailable(context.Background(), group) {
		t.Fatal("group-safe memory tool must be available in a group runner")
	}

	dm := RunnerParams{UserID: "user-1", AgentID: "agent-1"}
	if !NonGroupBuiltinToolAvailable(context.Background(), dm) {
		t.Fatal("private memory tool must remain available in a DM runner")
	}
	if GroupBuiltinToolAvailable(context.Background(), dm) {
		t.Fatal("group-safe memory tool must be unavailable in a DM runner")
	}
}
