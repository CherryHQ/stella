package agent

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
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
			if got := messageActor(tc.authority, memory.CurrentSpeaker{}, "source-session"); got != tc.want {
				t.Fatalf("actor=%#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestMessageActorUsesGroupHumanSpeakerNotExecutingAgent(t *testing.T) {
	authority, err := authz.NewGroupAgentAuthority("group-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	actor := messageActor(authority, memory.CurrentSpeaker{UserID: "speaker-1", PlatformUserID: "platform-speaker"}, "source-session")
	if want := (eventlog.MessageActor{Type: eventlog.ActorHuman, ID: "speaker-1"}); actor != want {
		t.Fatalf("group input actor=%#v, want %#v", actor, want)
	}
	if rendered := eventlog.RenderInput("human group message", actor); rendered != "human group message" {
		t.Fatalf("human group input rendered with agent envelope: %#v", rendered)
	}
}
