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
	b, _, captured := newThreadRoutingBotWithHandler(t)
	return b, captured
}

func newThreadRoutingBotWithHandler(t *testing.T) (*Bot, *mockHandler, <-chan channel.IncomingMessage) {
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
	return b, h, captured
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
	return receiveEvent(chatID, chatType, messageID, rootID, parentID, "text", `{"text":"`+text+`"}`)
}

func fileReceiveEvent(chatID, chatType, messageID, rootID, parentID string) *larkim.P2MessageReceiveV1 {
	return receiveEvent(chatID, chatType, messageID, rootID, parentID, "file", `{"file_name":"report.pdf"}`)
}

func receiveEvent(chatID, chatType, messageID, rootID, parentID, msgType, content string) *larkim.P2MessageReceiveV1 {
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

	event := textReceiveEvent("oc_chat", "group", "om_msg", "om_root", "om_parent", "hello thread")
	if err := b.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage: %v", err)
	}

	msg := waitMessage(t, captured)
	if !msg.IsGroup {
		t.Error("IsGroup = false, want true")
	}
	if msg.ChatID != "oc_chat" {
		t.Errorf("ChatID = %q, want %q", msg.ChatID, "oc_chat")
	}
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

	event := textReceiveEvent("oc_chat", "group", "om_msg", "", "", "hello")
	if err := b.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage: %v", err)
	}

	msg := waitMessage(t, captured)
	if !msg.IsGroup {
		t.Error("IsGroup = false, want true")
	}
	if msg.ChatID != "oc_chat" {
		t.Errorf("ChatID = %q, want %q", msg.ChatID, "oc_chat")
	}
	if msg.ThreadID != "" {
		t.Errorf("ThreadID = %q, want empty", msg.ThreadID)
	}
}

// TestOnMessageAuthPreservesThreadID proves the synthetic /auth message inherits
// the originating thread metadata so the OAuth turn stays in the same thread.
func TestOnMessageAuthPreservesThreadID(t *testing.T) {
	b, captured := newThreadRoutingBot(t)

	event := textReceiveEvent("oc_chat", "group", "om_msg", "om_root", "om_parent", "/auth")
	mentionName := "Agent"
	mentionOpenID := "ou_bot"
	event.Event.Message.Mentions = []*larkim.MentionEvent{{
		Name: &mentionName,
		Id:   &larkim.UserId{OpenId: &mentionOpenID},
	}}
	if err := b.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage: %v", err)
	}

	msg := waitMessage(t, captured)
	if !msg.IsGroup {
		t.Error("IsGroup = false, want true")
	}
	if msg.ChatID != "oc_chat" {
		t.Errorf("ChatID = %q, want %q", msg.ChatID, "oc_chat")
	}
	if msg.ThreadID != "om_root" {
		t.Errorf("ThreadID = %q, want %q", msg.ThreadID, "om_root")
	}
	if msg.MessageID != "om_msg" {
		t.Errorf("MessageID = %q, want %q", msg.MessageID, "om_msg")
	}
	if msg.ReplyTo != "om_parent" {
		t.Errorf("ReplyTo = %q, want %q", msg.ReplyTo, "om_parent")
	}
	if !msg.Timestamp.Equal(time.UnixMilli(1700000000000).UTC()) {
		t.Errorf("Timestamp = %v, want %v", msg.Timestamp, time.UnixMilli(1700000000000).UTC())
	}
	if len(msg.Mentions) != 1 || msg.Mentions[0].Raw != "Agent" || msg.Mentions[0].PlatformID != "ou_bot" {
		t.Errorf("Mentions = %#v, want Agent/ou_bot", msg.Mentions)
	}
}

func TestOnMessageFileResolveUserRootPreservesThreadID(t *testing.T) {
	b, h, captured := newThreadRoutingBotWithHandler(t)
	probeCh := make(chan channel.IncomingMessage, 1)
	h.resolveUserRootFn = func(_ context.Context, msg channel.IncomingMessage) (string, error) {
		probeCh <- msg
		return t.TempDir(), nil
	}

	event := fileReceiveEvent("oc_chat", "group", "om_file", "om_root", "om_parent")
	if err := b.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage: %v", err)
	}

	probe := waitMessage(t, probeCh)
	if !probe.IsGroup {
		t.Error("probe IsGroup = false, want true")
	}
	if probe.ChatID != "oc_chat" {
		t.Errorf("probe ChatID = %q, want %q", probe.ChatID, "oc_chat")
	}
	if probe.ThreadID != "om_root" {
		t.Errorf("probe ThreadID = %q, want %q", probe.ThreadID, "om_root")
	}

	msg := waitMessage(t, captured)
	if msg.ThreadID != "om_root" {
		t.Errorf("ThreadID = %q, want %q", msg.ThreadID, "om_root")
	}
}
