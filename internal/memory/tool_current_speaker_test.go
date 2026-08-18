package memory_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

// groupSpeakerCtx builds a group-turn context with no session user. Current
// speaker metadata must never become authority for private memory actions.
func groupSpeakerCtx(agentID string, speaker memory.CurrentSpeaker) context.Context {
	ctx := authz.WithAgentID(context.Background(), agentID)
	ctx = authz.WithGroupID(ctx, "group-1")
	ctx = memory.WithGroupSeq(ctx, 10)
	return memory.WithCurrentSpeaker(ctx, speaker)
}

func TestMemoryToolCurrentSpeaker_ProfileActionsFailClosed(t *testing.T) {
	fake := memorytest.New()
	tool := memory.BuildTool(fake)

	ctx := groupSpeakerCtx("agent1", memory.CurrentSpeaker{
		Platform:       "telegram",
		PlatformUserID: "tg-9",
		DisplayName:    "Alice",
		UserID:         "speaker1",
	})

	for _, args := range []map[string]any{
		{"action": "profile_get"},
		{"action": "profile_update", "content": "must not be written"},
	} {
		if _, err := tool.Execute(ctx, args); err == nil || !containsString(err.Error(), "no user context") {
			t.Fatalf("group profile action %#v error=%v", args, err)
		}
	}
	if stored, _ := fake.GetProfile(ctx, "speaker1", "agent1"); stored != "" {
		t.Errorf("group action wrote speaker profile %q", stored)
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
		if !containsString(err.Error(), "no user context") {
			t.Errorf("%s: expected 'no user context', got %q", action, err.Error())
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

func TestUnifiedMemoryGroupRecallDoesNotUseSpeakerPrivateMemory(t *testing.T) {
	fake := memorytest.New()
	ctx := groupSpeakerCtx("agent1", memory.CurrentSpeaker{UserID: "speaker1"})
	if err := fake.SetProfile(ctx, "speaker1", "agent1", "Speaker likes tea"); err != nil {
		t.Fatal(err)
	}
	tool := memory.BuildTool(fake, memory.WithRecallSource(&fakeRecallSource{}))

	for _, args := range []map[string]any{
		{"action": "read", "ref": "profile"},
		{"action": "read", "ref": "soul"},
		{"action": "read", "ref": "constraints"},
	} {
		if _, err := tool.Execute(ctx, args); err == nil {
			t.Fatalf("group unified action unexpectedly widened access: %#v", args)
		}
	}
	result, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "tea"})
	if err != nil {
		t.Fatalf("group public-history search: %v", err)
	}
	var search struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal([]byte(result), &search); err != nil || len(search.Results) != 0 {
		t.Fatalf("group public-history search result=%q parse_err=%v", result, err)
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
			t.Errorf("%s: expected fail-closed error in group context", action)
		}
	}

	// No participant-private state should have been written.
	if soul, _ := fake.GetAgentSoul(ctx, "speaker1", "agent1"); soul != "" {
		t.Errorf("speaker soul must not be writable from a group, got %q", soul)
	}
	if cs, _ := fake.GetConstraints(ctx, "speaker1", "agent1"); len(cs) != 0 {
		t.Errorf("speaker constraints must not be writable from a group, got %v", cs)
	}
}
