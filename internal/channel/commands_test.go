package channel

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func TestHandleCommandHelp(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	resp, ok := HandleCommand(context.Background(), rc, "/help", "user1")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != pkgchannel.WelcomeMessage {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleCommandStart(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	resp, ok := HandleCommand(context.Background(), rc, "/start", "user1")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != pkgchannel.WelcomeMessage {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleCommandWhoami(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	resp, ok := HandleCommand(context.Background(), rc, "/whoami", "SENDER_123")
	if !ok {
		t.Fatal("expected handled")
	}
	if resp != "Your ID: SENDER_123" {
		t.Errorf("response = %q", resp)
	}
}

func TestHandleCommandUnknown(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	_, ok := HandleCommand(context.Background(), rc, "hello world", "user1")
	if ok {
		t.Error("regular text should not be handled")
	}
}

func TestHandleCommandEmpty(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	_, ok := HandleCommand(context.Background(), rc, "", "user1")
	if ok {
		t.Error("empty text should not be handled")
	}
}

func TestHandleCommandModel(t *testing.T) {
	rc := &ResolvedChat{SessionKey: "ch"}
	_, ok := HandleCommand(context.Background(), rc, "/model gpt-4", "user1")
	if ok {
		t.Error("/model should NOT be handled (left to channels)")
	}
}

func TestWelcomeMessageIncludesNaturalLanguagePhrases(t *testing.T) {
	// Verify the WelcomeMessage explains natural-language shortcuts
	if !strings.Contains(pkgchannel.WelcomeMessage, "new session") {
		t.Error("WelcomeMessage should mention 'new session' as natural-language phrase")
	}
	if !strings.Contains(pkgchannel.WelcomeMessage, "新会话") {
		t.Error("WelcomeMessage should mention Chinese examples like '新会话'")
	}
	if !strings.Contains(pkgchannel.WelcomeMessage, "取消") {
		t.Error("WelcomeMessage should mention Chinese abort examples like '取消'")
	}
	if !strings.Contains(pkgchannel.WelcomeMessage, "When enabled") {
		t.Error("WelcomeMessage should clarify that natural-language shortcuts are conditional")
	}
	if !strings.Contains(pkgchannel.WelcomeMessage, "If a short phrase is unclear") {
		t.Error("WelcomeMessage should explain the fallback behavior for unclear phrases")
	}
}

// --- /compact group vs private mapping --------------------------------------

func newCompactTestChat(t *testing.T, groupID string, user auth.User) *ResolvedChat {
	t.Helper()
	const agentID = "cmd-agent"
	fake := memorytest.New()
	reg, err := session.NewRegistry(fake, agentID)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	rt, err := agentruntime.New(agentruntime.Config{
		Memory:    fake,
		NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	svc := &agent.Service{Sessions: reg, Runtime: rt, AgentID: agentID}

	rc := &ResolvedChat{Service: svc, AgentID: agentID, User: user, GroupID: groupID}
	if groupID != "" {
		rc.SessionKey = agent.BuildGroupSessionKey(agentID, groupID)
		rc.Channel = session.Channel("group:" + groupID)
	} else {
		rc.SessionKey = agent.BuildSessionKey(agentID, "telegram", user.ID, "private")
		rc.Channel = session.Channel("telegram")
	}
	return rc
}

// TestHandleCommandCompactGroupUsesGroupMemoryMessage proves a group /compact maps
// agent.ErrGroupCompactionUnsupported to the shared clear message, not the generic
// "Compaction failed".
func TestHandleCommandCompactGroupUsesGroupMemoryMessage(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	rc := newCompactTestChat(t, groupID, auth.User{})

	resp, ok := HandleCommand(context.Background(), rc, "/compact", "sender-1")
	if !ok {
		t.Fatal("expected /compact to be handled")
	}
	if resp != pkgchannel.GroupCompactUnsupportedMessage {
		t.Fatalf("response = %q, want the shared group-memory message", resp)
	}
	if strings.Contains(resp, "Compaction failed") {
		t.Fatal("group /compact must not surface the generic failure message")
	}
}

// TestHandleCommandCompactPrivateStillCompacts confirms the private /compact path
// is unchanged (the fake provider compacts successfully).
func TestHandleCommandCompactPrivateStillCompacts(t *testing.T) {
	rc := newCompactTestChat(t, "", auth.User{ID: "user-1"})

	resp, ok := HandleCommand(context.Background(), rc, "/compact", "user-1")
	if !ok {
		t.Fatal("expected /compact to be handled")
	}
	if resp != "Session compacted." {
		t.Fatalf("response = %q, want %q", resp, "Session compacted.")
	}
}
