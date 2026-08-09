package agent

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/eventlog"
)

func TestMessageActorUsesTrustedAuthorityAndSourceSession(t *testing.T) {
	user, err := authz.NewUserAuthority("user-1", false)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := authz.NewAgentAuthority("user-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	system, err := authz.NewSystemAuthority("scheduler")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		authority authz.Authority
		want      eventlog.MessageActor
	}{
		{name: "human", authority: user, want: eventlog.MessageActor{Type: eventlog.ActorHuman, ID: "user-1"}},
		{name: "agent", authority: agent, want: eventlog.MessageActor{Type: eventlog.ActorAgent, ID: "agent-1", SourceSessionID: "source-session"}},
		{name: "system", authority: system, want: eventlog.MessageActor{Type: eventlog.ActorSystem, ID: "scheduler"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageActor(tc.authority, "source-session"); got != tc.want {
				t.Fatalf("actor=%#v, want %#v", got, tc.want)
			}
		})
	}
}
