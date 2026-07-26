package sessionctl

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/agentctx"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
)

var nonceRE = regexp.MustCompile(`nonce: ([0-9a-f-]{36})`)

func requestNew(t *testing.T, f *fixture, ctx context.Context) string {
	t.Helper()
	out, err := f.tool.Execute(ctx, map[string]any{"action": ActionRequestNew})
	if err != nil {
		t.Fatalf("request_new: %v", err)
	}
	m := nonceRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("request_new returned no nonce: %q", out)
	}
	return m[1]
}

func confirmNew(f *fixture, ctx context.Context, nonce string) (string, error) {
	return f.tool.Execute(ctx, map[string]any{"action": ActionConfirmNew, "nonce": nonce})
}

// TestDMRequestThenConfirmRotatesMain is the happy path: the agent asks in one
// turn, the user answers in the next, and the chat lands on a fresh session.
func TestDMRequestThenConfirmRotatesMain(t *testing.T) {
	f := newFixture(t)
	askCtx, before := f.dmTurn(t, newTurnID())
	nonce := requestNew(t, f, askCtx)

	// request_new alone must change nothing.
	stillThere, err := f.svc.ResolveMainSession(context.Background(), mustUserAuthority(t), testUserID, testAgentID)
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	if stillThere.ID != before.ID {
		t.Fatal("request_new must not rotate on its own")
	}

	answerCtx := dmTurnCtx(before.ID, newTurnID())
	reply, err := confirmNew(f, answerCtx, nonce)
	if err != nil {
		t.Fatalf("confirm_new: %v", err)
	}
	if !strings.Contains(reply, "NEXT message") {
		t.Fatalf("confirm reply should tell the model when the new session starts, got %q", reply)
	}

	after, err := f.svc.ResolveMainSession(context.Background(), mustUserAuthority(t), testUserID, testAgentID)
	if err != nil {
		t.Fatalf("resolve main after: %v", err)
	}
	if after.ID == before.ID {
		t.Fatal("confirm_new must move the chat onto a new session")
	}
	if after.Archived {
		t.Fatal("the successor must be active")
	}
}

// TestConfirmInSameTurnRejected is the core of the two-phase gate: without an
// intervening user turn nobody has answered, so the reset must not happen — and
// the nonce must survive for the real answer.
func TestConfirmInSameTurnRejected(t *testing.T) {
	f := newFixture(t)
	turnID := newTurnID()
	ctx, before := f.dmTurn(t, turnID)
	nonce := requestNew(t, f, ctx)

	if _, err := confirmNew(f, ctx, nonce); err == nil {
		t.Fatal("confirm_new in the issuing turn must be rejected")
	} else if !strings.Contains(err.Error(), "same turn") {
		t.Fatalf("rejection should name the reason, got %v", err)
	}

	current, err := f.svc.ResolveMainSession(context.Background(), mustUserAuthority(t), testUserID, testAgentID)
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	if current.ID != before.ID {
		t.Fatal("a rejected confirmation must not rotate")
	}

	// The premature call must not burn the pending question.
	if _, err := confirmNew(f, dmTurnCtx(before.ID, newTurnID()), nonce); err != nil {
		t.Fatalf("nonce should survive a premature confirm, got %v", err)
	}
}

func TestConfirmExpiredNonceRejected(t *testing.T) {
	f := newFixture(t)
	_, before := f.dmTurn(t, newTurnID())
	nonce := requestNew(t, f, dmTurnCtx(before.ID, newTurnID()))

	expired := f.store.nonces[nonce]
	expired.ExpiresAt = time.Now().UTC().Add(-time.Second)
	f.store.nonces[nonce] = expired

	_, err := confirmNew(f, dmTurnCtx(before.ID, newTurnID()), nonce)
	if err == nil || !strings.Contains(err.Error(), "no longer valid") {
		t.Fatalf("expired nonce should be reported as stale, got %v", err)
	}
	current, _ := f.svc.ResolveMainSession(context.Background(), mustUserAuthority(t), testUserID, testAgentID)
	if current.ID != before.ID {
		t.Fatal("an expired confirmation must not rotate")
	}
}

// TestConfirmUsedNonceRejected proves single use: replaying a confirmation must
// not rotate the successor the first one just created.
func TestConfirmUsedNonceRejected(t *testing.T) {
	f := newFixture(t)
	_, before := f.dmTurn(t, newTurnID())
	nonce := requestNew(t, f, dmTurnCtx(before.ID, newTurnID()))

	if _, err := confirmNew(f, dmTurnCtx(before.ID, newTurnID()), nonce); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	first, _ := f.svc.ResolveMainSession(context.Background(), mustUserAuthority(t), testUserID, testAgentID)

	if _, err := confirmNew(f, dmTurnCtx(before.ID, newTurnID()), nonce); err == nil {
		t.Fatal("a used nonce must not confirm twice")
	}
	second, _ := f.svc.ResolveMainSession(context.Background(), mustUserAuthority(t), testUserID, testAgentID)
	if second.ID != first.ID {
		t.Fatal("a replayed confirmation must not rotate again")
	}
}

// TestConfirmAfterExternalRotationIsStale covers a `/new` landing between the
// question and the answer: there is nothing left to reset, and the tool must say
// so rather than rotate the fresh session away.
func TestConfirmAfterExternalRotationIsStale(t *testing.T) {
	f := newFixture(t)
	_, before := f.dmTurn(t, newTurnID())
	nonce := requestNew(t, f, dmTurnCtx(before.ID, newTurnID()))

	rotated, err := f.svc.RotateMainSession(context.Background(), mustUserAuthority(t), testUserID, testAgentID, before.ID)
	if err != nil {
		t.Fatalf("external rotation: %v", err)
	}

	_, err = confirmNew(f, dmTurnCtx(rotated.ID, newTurnID()), nonce)
	if err == nil || !strings.Contains(err.Error(), "no longer valid") {
		t.Fatalf("stale nonce should report a clean no-op, got %v", err)
	}
	current, _ := f.svc.ResolveMainSession(context.Background(), mustUserAuthority(t), testUserID, testAgentID)
	if current.ID != rotated.ID {
		t.Fatal("a stale confirmation must leave the current session alone")
	}
}

// TestGroupConfirmRequiresRequestingSpeaker: a group's context is shared, so
// only the member who asked can spend the confirmation.
func TestGroupConfirmRequiresRequestingSpeaker(t *testing.T) {
	f := newFixture(t)
	askCtx, before := f.groupTurn(t, "speaker-a", 10)
	nonce := requestNew(t, f, askCtx)

	_, err := confirmNew(f, groupTurnCtx(before.ID, "speaker-b", 11), nonce)
	if err == nil || !strings.Contains(err.Error(), "only the person who asked") {
		t.Fatalf("another speaker must not confirm, got %v", err)
	}
	unchanged, err := f.svc.ResolveChatChannelSession(context.Background(), groupChannelRequest(t))
	if err != nil {
		t.Fatalf("resolve group session: %v", err)
	}
	if unchanged.ID != before.ID {
		t.Fatal("a foreign speaker's confirmation must not rotate the group")
	}

	if _, err := confirmNew(f, groupTurnCtx(before.ID, "speaker-a", 12), nonce); err != nil {
		t.Fatalf("the requesting speaker must be able to confirm: %v", err)
	}
	after, err := f.svc.ResolveChatChannelSession(context.Background(), groupChannelRequest(t))
	if err != nil {
		t.Fatalf("resolve group session after: %v", err)
	}
	if after.ID == before.ID {
		t.Fatal("group confirm_new must rotate this agent's group session")
	}
	if after.GroupID != testGroupID || after.Archived {
		t.Fatalf("successor = %+v, want an active session owned by the group", after)
	}
}

// TestGroupConfirmRejectsRedeliveredTurn: the durable dispatcher can retry the
// same group message. A retry is not a new user answer, and the event-log seq is
// what keeps it from counting as one.
func TestGroupConfirmRejectsRedeliveredTurn(t *testing.T) {
	f := newFixture(t)
	askCtx, before := f.groupTurn(t, "speaker-a", 42)
	nonce := requestNew(t, f, askCtx)

	_, err := confirmNew(f, groupTurnCtx(before.ID, "speaker-a", 42), nonce)
	if err == nil || !strings.Contains(err.Error(), "same turn") {
		t.Fatalf("a redelivered message must not count as an answer, got %v", err)
	}
}

// TestRefusesTurnsWithoutChatBinding is the exposure guard. Every surface that
// is not a chat channel — the Web UI, the API, webhooks, scheduler/task/delegate
// runs — reaches the tool without a chat binding and must be refused, including
// on the very main session a DM shares with the Web UI.
func TestRefusesTurnsWithoutChatBinding(t *testing.T) {
	f := newFixture(t)
	_, main := f.dmTurn(t, newTurnID())

	base := authz.WithUserID(context.Background(), testUserID)
	base = authz.WithAgentID(base, testAgentID)
	base = agentctx.WithTurnID(base, newTurnID())

	cases := map[string]context.Context{
		// A Web tab on the shared main session: same session id, no binding.
		"web session":       memory.WithSessionID(base, main.ID),
		"scheduler session": memory.WithSessionID(base, "sched-"+testUserID),
		"task session":      memory.WithSessionID(base, "task-"+testUserID),
		"delegate session":  memory.WithSessionID(base, "delegate-"+testUserID),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			for _, action := range []string{ActionRequestNew, ActionConfirmNew, ActionCompact} {
				_, err := f.tool.Execute(ctx, map[string]any{"action": action, "nonce": "x"})
				if err == nil {
					t.Fatalf("%s must be refused outside a chat channel", action)
				}
				if !strings.Contains(err.Error(), "only available in a chat channel") {
					t.Fatalf("%s refusal should explain itself, got %v", action, err)
				}
			}
		})
	}

	if len(f.store.nonces) != 0 {
		t.Fatal("a refused turn must not record a pending rotation")
	}
}

// TestRefusesGroupTurnWithoutIdentifiableSpeaker: with no speaker there is
// nobody whose confirmation would count, so the two-phase gate cannot hold.
func TestRefusesGroupTurnWithoutIdentifiableSpeaker(t *testing.T) {
	f := newFixture(t)
	_, before := f.groupTurn(t, "speaker-a", 1)

	ctx := authz.WithGroupID(context.Background(), testGroupID)
	ctx = authz.WithAgentID(ctx, testAgentID)
	ctx = memory.WithSessionID(ctx, before.ID)
	ctx = memory.WithGroupSeq(ctx, 2)
	ctx = memory.WithCurrentSpeaker(ctx, memory.CurrentSpeaker{})
	ctx = agentctx.WithChatBinding(ctx, agentctx.ChatBinding{Channel: "group:" + testGroupID})

	if _, err := f.tool.Execute(ctx, map[string]any{"action": ActionRequestNew}); err == nil {
		t.Fatal("an unidentifiable group sender must not be able to reset the group")
	}
}

func TestUnknownActionIsRejected(t *testing.T) {
	f := newFixture(t)
	ctx, _ := f.dmTurn(t, newTurnID())
	if _, err := f.tool.Execute(ctx, map[string]any{"action": "rotate_everything"}); err == nil {
		t.Fatal("unknown action must be rejected")
	}
}

func TestDefinitionAdvertisesEveryAction(t *testing.T) {
	f := newFixture(t)
	def := f.tool.Definition()
	if def.Name != ToolName {
		t.Fatalf("tool name = %q", def.Name)
	}
	for _, action := range []string{ActionRequestNew, ActionConfirmNew, ActionCompact} {
		if !strings.Contains(def.Description, action) {
			t.Fatalf("description omits %q", action)
		}
	}
}
