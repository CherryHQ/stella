package channel

import (
	"testing"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestPlatformGroupSpeaker_Linked(t *testing.T) {
	msg := pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "tg-9", SenderName: "Alice"}
	sp := platformGroupSpeaker(msg, "user-1", "Alice Stored")

	if sp.UserID != "user-1" {
		t.Errorf("linked speaker UserID = %q, want user-1", sp.UserID)
	}
	if sp.Platform != "telegram" || sp.PlatformUserID != "tg-9" {
		t.Errorf("speaker platform fields = %+v", sp)
	}
	if sp.DisplayName != "Alice" {
		t.Errorf("DisplayName = %q, want live platform name Alice", sp.DisplayName)
	}
}

func TestPlatformGroupSpeaker_UnlinkedHasNoUserID(t *testing.T) {
	msg := pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "tg-stranger", SenderName: "Stranger"}
	// Unlinked: no resolved auth user.
	sp := platformGroupSpeaker(msg, "", "")

	if sp.UserID != "" {
		t.Errorf("unlinked speaker must have empty UserID, got %q", sp.UserID)
	}
	if sp.DisplayName != "Stranger" {
		t.Errorf("DisplayName = %q, want Stranger", sp.DisplayName)
	}
	if sp.PlatformUserID != "tg-stranger" {
		t.Errorf("PlatformUserID = %q", sp.PlatformUserID)
	}
}

func TestPlatformGroupSpeaker_DisplayNameFallsBackToStored(t *testing.T) {
	// Delayed retry loses the live SenderName; fall back to the stored user name.
	msg := pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "tg-9"}
	sp := platformGroupSpeaker(msg, "user-1", "Alice Stored")
	if sp.DisplayName != "Alice Stored" {
		t.Errorf("DisplayName = %q, want fallback Alice Stored", sp.DisplayName)
	}
}

func TestWebGroupSpeaker_HumanActorIsTarget(t *testing.T) {
	msg := sqlc.CtxGroupMessage{ActorType: string(eventlog.ActorHuman), ActorID: "web-user-7"}
	sp := webGroupSpeaker(msg)

	if sp.UserID != "web-user-7" || sp.PlatformUserID != "web-user-7" {
		t.Errorf("web human speaker = %+v, want UserID/PlatformUserID web-user-7", sp)
	}
	if sp.Platform != "web" {
		t.Errorf("Platform = %q, want web", sp.Platform)
	}
}

func TestWebGroupSpeaker_NonHumanFailsClosed(t *testing.T) {
	cases := []sqlc.CtxGroupMessage{
		{ActorType: string(eventlog.ActorAgent), ActorID: "agent-1"}, // not a human
		{ActorType: string(eventlog.ActorHuman), ActorID: ""},        // missing id
		{ActorType: "", ActorID: "web-user-7"},                       // malformed row
	}
	for i, msg := range cases {
		if sp := webGroupSpeaker(msg); sp != (memory.CurrentSpeaker{}) {
			t.Errorf("case %d: expected zero speaker (fail closed), got %+v", i, sp)
		}
	}
}

func TestSystemInputHasEmptySpeaker(t *testing.T) {
	speaker, actor := groupMessageProvenance(sqlc.CtxGroupMessage{ActorType: string(eventlog.ActorSystem), ActorID: "nudge"}, memory.CurrentSpeaker{UserID: "alice", PlatformUserID: "tg-alice"})
	if speaker != (memory.CurrentSpeaker{}) {
		t.Fatalf("system speaker = %+v, want empty", speaker)
	}
	if actor != (eventlog.MessageActor{Type: eventlog.ActorSystem, ID: "nudge"}) {
		t.Fatalf("system input actor = %+v", actor)
	}
}
