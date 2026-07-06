package memory_test

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

// snapshotSpy records every AdvanceSessionSnapshot call so tests can assert which
// (sessionID, userID, agentID) row the memory tool advances.
type snapshotSpy struct {
	*memorytest.Fake
	advanced [][3]string
}

func (s *snapshotSpy) AdvanceSessionSnapshot(ctx context.Context, sessionID, userID, agentID string) error {
	s.advanced = append(s.advanced, [3]string{sessionID, userID, agentID})
	return s.Fake.AdvanceSessionSnapshot(ctx, sessionID, userID, agentID)
}

// groupSpeakerCtx builds a group-turn context: no session user (D9), agent set,
// and a linked current speaker.
func groupSpeakerCtx(agentID string, speaker memory.CurrentSpeaker) context.Context {
	ctx := authz.WithAgentID(context.Background(), agentID)
	return memory.WithCurrentSpeaker(ctx, speaker)
}

func TestMemoryToolCurrentSpeaker_LinkedProfileGetUpdate(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)

	ctx := groupSpeakerCtx("agent1", memory.CurrentSpeaker{
		Platform:       "telegram",
		PlatformUserID: "tg-9",
		DisplayName:    "Alice",
		UserID:         "speaker1",
	})

	result, err := tool.Execute(ctx, map[string]any{"action": "profile_get"})
	if err != nil {
		t.Fatalf("profile_get: %v", err)
	}
	if result != "No profile notes found." {
		t.Errorf("expected empty profile message, got %q", result)
	}

	if _, err := tool.Execute(ctx, map[string]any{"action": "profile_update", "content": "Likes Go and tea"}); err != nil {
		t.Fatalf("profile_update: %v", err)
	}

	result, err = tool.Execute(ctx, map[string]any{"action": "profile_get"})
	if err != nil {
		t.Fatalf("profile_get after update: %v", err)
	}
	if result != "Likes Go and tea" {
		t.Errorf("expected speaker profile, got %q", result)
	}

	// The write must target the speaker's auth user, not the (empty) session user.
	stored, _ := fake.GetProfile(ctx, "speaker1", "agent1")
	if stored != "Likes Go and tea" {
		t.Errorf("expected profile stored under speaker1, got %q", stored)
	}
}

func TestMemoryToolCurrentSpeaker_UnlinkedFailsClosed(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)

	// Speaker present but unlinked: UserID empty.
	ctx := groupSpeakerCtx("agent1", memory.CurrentSpeaker{
		Platform:       "telegram",
		PlatformUserID: "tg-stranger",
		DisplayName:    "Stranger",
	})

	for _, action := range []string{"profile_get", "profile_update"} {
		args := map[string]any{"action": action}
		if action == "profile_update" {
			args["content"] = "should not be written"
		}
		_, err := tool.Execute(ctx, args)
		if err == nil {
			t.Fatalf("%s: expected fail-closed error for unlinked speaker", action)
		}
		if !containsString(err.Error(), "no linked current speaker") {
			t.Errorf("%s: expected 'no linked current speaker', got %q", action, err.Error())
		}
	}
}

func TestMemoryToolCurrentSpeaker_DMUnchanged(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)

	// Session user is set; a speaker is also present but must be ignored.
	ctx := authz.WithUserID(context.Background(), "42")
	ctx = authz.WithAgentID(ctx, "agent1")
	ctx = memory.WithCurrentSpeaker(ctx, memory.CurrentSpeaker{UserID: "speaker1"})

	if _, err := tool.Execute(ctx, map[string]any{"action": "profile_update", "content": "DM note"}); err != nil {
		t.Fatalf("profile_update: %v", err)
	}

	dmProfile, _ := fake.GetProfile(ctx, "42", "agent1")
	if dmProfile != "DM note" {
		t.Errorf("expected DM note under session user, got %q", dmProfile)
	}
	speakerProfile, _ := fake.GetProfile(ctx, "speaker1", "agent1")
	if speakerProfile != "" {
		t.Errorf("session user must win; speaker profile should be untouched, got %q", speakerProfile)
	}
}

func TestMemoryToolCurrentSpeaker_ForbiddenActionsFailClosed(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)

	ctx := groupSpeakerCtx("agent1", memory.CurrentSpeaker{UserID: "speaker1"})

	cases := []map[string]any{
		{"action": "soul_get"},
		{"action": "soul_update", "content": "new soul"},
		{"action": "constraint_list"},
		{"action": "constraint_add", "constraint_text": "no swearing"},
		{"action": "profile_history", "history_scope": "profile"},
		{"action": "profile_rollback", "history_scope": "profile", "rollback_version": 1},
	}
	for _, args := range cases {
		action := args["action"].(string)
		if _, err := tool.Execute(ctx, args); err == nil {
			t.Errorf("%s: expected fail-closed error under current-speaker fallback", action)
		}
	}

	// No speaker soul or constraints should have been written.
	if soul, _ := fake.GetAgentSoul(ctx, "speaker1", "agent1"); soul != "" {
		t.Errorf("speaker soul must not be writable via fallback, got %q", soul)
	}
	if cs, _ := fake.GetConstraints(ctx, "speaker1", "agent1"); len(cs) != 0 {
		t.Errorf("speaker constraints must not be writable via fallback, got %v", cs)
	}
}

func TestMemoryToolCurrentSpeaker_SnapshotAdvancesSpeakerRow(t *testing.T) {
	spy := &snapshotSpy{Fake: memorytest.New()}
	tool := memory.BuildTool(spy)

	const sessionID = "group-sess-1"
	bg := context.Background()
	// Seed the speaker snapshot row so an advance has a row to move.
	if _, err := spy.GetOrCreateSessionSnapshot(bg, sessionID, "speaker1", "agent1"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	ctx := memory.WithSessionID(bg, sessionID)
	ctx = authz.WithAgentID(ctx, "agent1")
	ctx = memory.WithCurrentSpeaker(ctx, memory.CurrentSpeaker{UserID: "speaker1"})

	if _, err := tool.Execute(ctx, map[string]any{"action": "profile_update", "content": "x"}); err != nil {
		t.Fatalf("profile_update: %v", err)
	}

	if len(spy.advanced) != 1 {
		t.Fatalf("expected exactly one snapshot advance, got %d: %v", len(spy.advanced), spy.advanced)
	}
	got := spy.advanced[0]
	if got != [3]string{sessionID, "speaker1", "agent1"} {
		t.Errorf("advance must target the speaker row, got %v", got)
	}
}
