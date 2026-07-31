package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/session"
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

func TestKnowledgeToolAvailable(t *testing.T) {
	tests := []struct {
		name string
		p    RunnerParams
		want bool
	}{
		{
			name: "private chat",
			p: RunnerParams{
				UserID:      "u1",
				AgentID:     "a1",
				SessionKind: string(session.KindChat),
			},
			want: true,
		},
		{
			name: "legacy main",
			p: RunnerParams{
				UserID:      "u1",
				AgentID:     "a1",
				SessionKind: string(session.KindMain),
			},
			want: true,
		},
		{
			name: "group",
			p: RunnerParams{
				UserID:      "u1",
				AgentID:     "a1",
				GroupID:     "g1",
				SessionKind: string(session.KindChat),
			},
			want: false,
		},
		{
			name: "task worker",
			p: RunnerParams{
				UserID:      "u1",
				AgentID:     "a1",
				SessionKind: string(session.KindTask),
			},
			want: false,
		},
		{
			name: "scheduler",
			p: RunnerParams{
				UserID:      "u1",
				AgentID:     "a1",
				SessionKind: string(session.KindScheduler),
			},
			want: false,
		},
		{
			name: "delegate",
			p: RunnerParams{
				UserID:      "u1",
				AgentID:     "a1",
				SessionKind: string(session.KindDelegate),
			},
			want: false,
		},
		{
			name: "no user",
			p: RunnerParams{
				AgentID:     "a1",
				SessionKind: string(session.KindChat),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KnowledgeToolAvailable(context.Background(), tt.p); got != tt.want {
				t.Fatalf("KnowledgeToolAvailable=%v want %v", got, tt.want)
			}
		})
	}
}
