package feishu

import (
	"context"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/CherryHQ/stella/pkg/channel"
)

// newThreadRoutingBot builds a Bot wired to a handler that records the
// IncomingMessage passed to HandleIncoming. The captured message is delivered
// on the returned channel so tests can assert on its thread metadata.
func newThreadRoutingBot(t *testing.T) (*Bot, <-chan channel.IncomingMessage) {
	t.Helper()
	captured := make(chan channel.IncomingMessage, 1)
	h := &mockHandler{
		handleIncomingFn: func(_ context.Context, msg channel.IncomingMessage, _, _ string) (string, bool, *channel.ChatStream, error) {
			captured <- msg
			return "", false, nil, nil
		},
	}
	b := &Bot{
		handler:     h,
		cfg:         Config{AppID: "a", AppSecret: "s"},
		chatModels:  make(map[string]channel.ModelOption),
		seenMsgs:    make(map[string]time.Time),
		provisioned: make(map[string]time.Time),
	}
	return b, captured
}

func waitMessage(t *testing.T, ch <-chan channel.IncomingMessage) channel.IncomingMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for handler to receive message")
		return channel.IncomingMessage{}
	}
}

func textReceiveEvent(chatID, chatType, messageID, rootID, parentID, text string) *larkim.P2MessageReceiveV1 {
	msgType := "text"
	content := `{"text":"` + text + `"}`
	createTime := "1700000000000"
	openID := "ou_sender"
	unionID := "on_sender"
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{OpenId: &openID, UnionId: &unionID},
			},
			Message: &larkim.EventMessage{
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageId:   &messageID,
				RootId:      &rootID,
				ParentId:    &parentID,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createTime,
			},
		},
	}
}

// TestOnMessageThreadIDFromRootID proves a threaded Feishu message resolves to
// a thread-scoped context: ThreadID carries root_id so the group/session layer
// keys on feishu + chat_id + root_id rather than the bare chat.
func TestOnMessageThreadIDFromRootID(t *testing.T) {
	b, captured := newThreadRoutingBot(t)

	event := textReceiveEvent("oc_chat", "p2p", "om_msg", "om_root", "om_parent", "hello thread")
	if err := b.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage: %v", err)
	}

	msg := waitMessage(t, captured)
	if msg.ThreadID != "om_root" {
		t.Errorf("ThreadID = %q, want %q", msg.ThreadID, "om_root")
	}
	if msg.MessageID != "om_msg" {
		t.Errorf("MessageID = %q, want %q", msg.MessageID, "om_msg")
	}
	if msg.ReplyTo != "om_parent" {
		t.Errorf("ReplyTo = %q, want %q", msg.ReplyTo, "om_parent")
	}
}

// TestOnMessageNoThreadID confirms a non-threaded message keeps ThreadID empty
// so it stays in the shared chat context.
func TestOnMessageNoThreadID(t *testing.T) {
	b, captured := newThreadRoutingBot(t)

	event := textReceiveEvent("oc_chat", "p2p", "om_msg", "", "", "hello")
	if err := b.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage: %v", err)
	}

	msg := waitMessage(t, captured)
	if msg.ThreadID != "" {
		t.Errorf("ThreadID = %q, want empty", msg.ThreadID)
	}
}

// TestOnMessageAuthPreservesThreadID proves the synthetic /auth message inherits
// the originating thread metadata so the OAuth turn stays in the same thread.
func TestOnMessageAuthPreservesThreadID(t *testing.T) {
	b, captured := newThreadRoutingBot(t)

	event := textReceiveEvent("oc_chat", "p2p", "om_msg", "om_root", "om_parent", "/auth")
	if err := b.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage: %v", err)
	}

	msg := waitMessage(t, captured)
	if msg.ThreadID != "om_root" {
		t.Errorf("ThreadID = %q, want %q", msg.ThreadID, "om_root")
	}
	if msg.MessageID != "om_msg" {
		t.Errorf("MessageID = %q, want %q", msg.MessageID, "om_msg")
	}
	if msg.ReplyTo != "om_parent" {
		t.Errorf("ReplyTo = %q, want %q", msg.ReplyTo, "om_parent")
	}
}
