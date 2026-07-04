package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/pkg/tools"
)

type namedTool string

func (n namedTool) Definition() tools.Definition                            { return tools.Definition{Name: string(n)} }
func (n namedTool) Execute(context.Context, map[string]any) (string, error) { return "{}", nil }

func TestDomainToolMountEnabledUsesContextPredicateFirst(t *testing.T) {
	ctx := context.Background()
	params := RunnerParams{UserID: "u1", AgentID: "a1"}
	calledLegacy := false
	mount := DomainToolMount{
		Name:      "test",
		Tool:      namedTool("test"),
		Predicate: func(RunnerParams) bool { calledLegacy = true; return false },
		PredicateCtx: func(got context.Context, p RunnerParams) bool {
			if got != ctx || p.UserID != params.UserID || p.AgentID != params.AgentID {
				t.Fatalf("PredicateCtx got ctx=%v params=%+v", got, p)
			}
			return true
		},
	}
	if !domainToolMountEnabled(ctx, mount, params) {
		t.Fatal("domainToolMountEnabled=false, want true")
	}
	if calledLegacy {
		t.Fatal("legacy Predicate should not run when PredicateCtx is set")
	}
}

func TestDomainToolMountEnabledFallsBackToLegacyPredicate(t *testing.T) {
	mount := DomainToolMount{Name: "test", Tool: namedTool("test"), Predicate: func(RunnerParams) bool { return false }}
	if domainToolMountEnabled(context.Background(), mount, RunnerParams{UserID: "u1", AgentID: "a1"}) {
		t.Fatal("domainToolMountEnabled=true, want false")
	}
}

func TestDomainToolAvailable(t *testing.T) {
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
			if got := DomainToolAvailable(tt.p); got != tt.want {
				t.Fatalf("DomainToolAvailable=%v want %v", got, tt.want)
			}
		})
	}
}
