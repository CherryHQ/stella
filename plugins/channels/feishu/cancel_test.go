package feishu

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/CherryHQ/stella/pkg/channel"
)

func TestCancelCardActionOnlyRequesterCanAbortBoundTurn(t *testing.T) {
	aborted := false
	control := &cancelControl{requesterID: "on_requester", abort: func() bool {
		aborted = true
		return true
	}}
	bot := &Bot{
		cfg:     Config{AllowDM: true},
		cancels: newCancelRegistry(),
		resolveMessageContextFn: func(string) (string, string, string, bool, bool) {
			return "oc_dm", "p2p", "", true, true
		},
	}
	bot.botOpenID.Store("ou_bot")
	bot.unionIDs.Store("ou_requester", "on_requester")
	token := bot.cancels.register("on_requester", "oc_dm", "", control)

	foreign := cancelActionEvent("ou_other", "oc_dm", "om_card", cancelCardActionPrefix+token)
	foreignResp, err := bot.onCardAction(context.Background(), foreign)
	if err != nil || foreignResp == nil || foreignResp.Toast == nil {
		t.Fatalf("foreign cancel response = %#v, %v", foreignResp, err)
	}
	if aborted {
		t.Fatal("foreign user aborted the response")
	}
	if _, ok := bot.cancels.get(token); !ok {
		t.Fatal("foreign click consumed the requester-bound action")
	}

	requester := cancelActionEvent("ou_requester", "oc_dm", "om_card", cancelCardActionPrefix+token)
	resp, err := bot.onCardAction(context.Background(), requester)
	if err != nil || resp == nil || resp.Toast == nil {
		t.Fatalf("requester cancel response = %#v, %v", resp, err)
	}
	if resp.Toast.Content != "Stopping…" || !aborted || !control.wasCancelled() {
		t.Fatalf("cancel result = toast %q aborted=%v cancelled=%v", resp.Toast.Content, aborted, control.wasCancelled())
	}
	if _, ok := bot.cancels.get(token); ok {
		t.Fatal("accepted cancellation left an active action token")
	}
}

func TestStreamCancelCardIsNativeAndIsRemovedAfterTurn(t *testing.T) {
	var content string
	bot := &Bot{
		cancels: newCancelRegistry(),
		replyCardFn: func(_ context.Context, _ string, card string) (string, error) {
			content = card
			return "om_progress", nil
		},
	}
	events := make(chan channel.Event)
	close(events)
	control := &cancelControl{requesterID: "on_requester", abort: func() bool { return true }}
	messageID, _, _, _, _, _, err := bot.streamResponseInThread(context.Background(), events, "oc_chat", "om_request", "om_root", control)
	if err != nil || messageID != "om_progress" {
		t.Fatalf("stream start = message %q, err %v", messageID, err)
	}
	if !strings.Contains(content, cancelCardActionPrefix) || !strings.Contains(content, `"content":"Cancel"`) {
		t.Fatalf("thinking card did not include the native cancel callback: %s", content)
	}
	if len(bot.cancels.entries) != 0 || len(bot.cancels.order) != 0 {
		t.Fatalf("completed turn retained cancel state: entries=%d order=%d", len(bot.cancels.entries), len(bot.cancels.order))
	}
}

func cancelActionEvent(openID, chatID, messageID, action string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: openID},
		Action:   &callback.CallBackAction{Value: map[string]any{"action": action}},
		Context:  &callback.Context{OpenChatID: chatID, OpenMessageID: messageID},
	}}
}

func TestCancelledTurnGetsTerminalResponse(t *testing.T) {
	var patched []string
	control := &cancelControl{requesterID: "on_requester", abort: func() bool { return true }}
	control.cancelled.Store(true)
	bot := &Bot{
		cancels:     newCancelRegistry(),
		replyCardFn: func(_ context.Context, _ string, _ string) (string, error) { return "om_progress", nil },
		patchCardFn: func(_ context.Context, _ string, content string) error {
			patched = append(patched, content)
			return nil
		},
	}
	events := make(chan channel.Event)
	close(events)
	_, _, _, _, _, _, err := bot.streamResponseInThread(context.Background(), events, "oc_chat", "om_request", "", control)
	if err != nil {
		t.Fatal(err)
	}
	if err := bot.sendFinalResponseInThread(context.Background(), "oc_chat", "om_request", "", "om_progress", "⏹️ Cancelled."+elapsedFooter(time.Second), nil, false, true); err != nil {
		t.Fatal(err)
	}
	if len(patched) != 1 || !strings.Contains(patched[0], "Cancelled") {
		t.Fatalf("terminal cancelled card patches = %v", patched)
	}
}
