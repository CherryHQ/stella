package feishu

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	internalchannel "github.com/CherryHQ/stella/internal/channel"
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
		cfg:         Config{AppID: "a", AppSecret: "s", AllowGroup: true, AllowDM: true, RequireMention: false},
		seenMsgs:    make(map[string]time.Time),
		provisioned: make(map[string]time.Time),
	}
	b.botOpenID.Store("ou_bot")
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

func assertNoMessage(t *testing.T, ch <-chan channel.IncomingMessage) {
	t.Helper()
	select {
	case msg := <-ch:
		t.Fatalf("unexpected forwarded message: %#v", msg)
	case <-time.After(50 * time.Millisecond):
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

func TestFeishuIngressAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       Config
		chatID    string
		chatType  string
		mentioned bool
		want      bool
	}{
		{name: "direct messages disabled", cfg: Config{}, chatID: "oc_dm", chatType: "p2p"},
		{name: "group chats disabled", cfg: Config{AllowGroup: false, RequireMention: false}, chatID: "oc_chat", chatType: "group"},
		{name: "allowed group requires mention", cfg: Config{AllowGroup: true, RequireMention: true}, chatID: "oc_chat", chatType: "group"},
		{name: "allowed mentioned group", cfg: Config{AllowGroup: true, RequireMention: true}, chatID: "oc_chat", chatType: "group", mentioned: true, want: true},
		{name: "direct messages enabled", cfg: Config{AllowDM: true}, chatID: "oc_dm", chatType: "p2p", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, captured := newThreadRoutingBot(t)
			b.cfg = tc.cfg
			b.botOpenID.Store("ou_bot")
			event := textReceiveEvent(tc.chatID, tc.chatType, "om_admission", "", "", "hello")
			if tc.mentioned {
				name, key, botID := "Stella", "@_user_1", "ou_bot"
				event.Event.Message.Mentions = []*larkim.MentionEvent{{Name: &name, Key: &key, Id: &larkim.UserId{OpenId: &botID}}}
			}
			if err := b.onMessage(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if tc.want {
				_ = waitMessage(t, captured)
			} else {
				assertNoMessage(t, captured)
			}
		})
	}
}

func TestNormalizeFeishuTopicChatTypes(t *testing.T) {
	for _, chatType := range []string{"topic", "topic_group", "group"} {
		if got := normalizeChatType(chatType); got != "group" {
			t.Errorf("normalizeChatType(%q) = %q, want group", chatType, got)
		}
	}
	b, _ := newThreadRoutingBot(t)
	b.chatTypes.Store("oc_topic", "topic_group")
	if got, ok := b.getChatType("oc_topic"); !ok || got != "group" {
		t.Fatalf("cached topic chat type = %q, %v; want group, true", got, ok)
	}
}

func TestReplyToBotMessageIsDirectedAfterAuthoritativeLookup(t *testing.T) {
	b, captured := newThreadRoutingBot(t)
	b.cfg.RequireMention = true
	b.resolveMessageContextFn = func(messageID string) (string, string, string, bool, bool) {
		if messageID != "om_parent" {
			t.Fatalf("resolved message %q, want parent", messageID)
		}
		return "oc_chat", "topic", "om_root", true, true
	}
	if err := b.onMessage(context.Background(), textReceiveEvent("oc_chat", "topic_group", "om_reply", "om_root", "om_parent", "reply")); err != nil {
		t.Fatal(err)
	}
	msg := waitMessage(t, captured)
	if !msg.IsGroup {
		t.Fatal("topic reply was not normalized to a group message")
	}
}

func TestReplyLookupFailureDoesNotBypassMentionRequirement(t *testing.T) {
	b, captured := newThreadRoutingBot(t)
	b.cfg.RequireMention = true
	b.resolveMessageContextFn = func(string) (string, string, string, bool, bool) {
		return "", "", "", false, false
	}
	if err := b.onMessage(context.Background(), textReceiveEvent("oc_chat", "group", "om_reply", "", "om_parent", "reply")); err != nil {
		t.Fatal(err)
	}
	assertNoMessage(t, captured)
}

func TestReplyLookupRunsOnlyWhenItCanChangeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      Config
		chatType string
		wantMsg  bool
	}{
		{name: "direct message", cfg: Config{AllowDM: true, RequireMention: true}, chatType: "p2p", wantMsg: true},
		{name: "disallowed group", cfg: Config{AllowGroup: false, RequireMention: true}, chatType: "group"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, captured := newThreadRoutingBot(t)
			b.cfg = tc.cfg
			lookups := 0
			b.resolveMessageContextFn = func(string) (string, string, string, bool, bool) {
				lookups++
				return "oc_chat", tc.chatType, "", true, true
			}
			if err := b.onMessage(context.Background(), textReceiveEvent("oc_chat", tc.chatType, "om_reply", "", "om_parent", "reply")); err != nil {
				t.Fatal(err)
			}
			if lookups != 0 {
				t.Fatalf("parent lookups = %d, want 0", lookups)
			}
			if tc.wantMsg {
				_ = waitMessage(t, captured)
			} else {
				assertNoMessage(t, captured)
			}
		})
	}
}

func TestAlternateEventIngressAdmission(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cfg         Config
		chatID      string
		chatType    string
		lookupOK    bool
		botAuthored bool
		want        bool
	}{
		{name: "disallowed group", cfg: Config{AllowGroup: false}, chatID: "oc_chat", chatType: "group", lookupOK: true},
		{name: "direct messages disabled", cfg: Config{}, chatID: "oc_dm", chatType: "p2p", lookupOK: true},
		{name: "lookup failure", cfg: Config{AllowDM: true}, lookupOK: false},
		{name: "reaction to user message does not bypass mention", cfg: Config{AllowGroup: true, RequireMention: true}, chatID: "oc_chat", chatType: "group", lookupOK: true},
		{name: "reaction to bot message bypasses mention", cfg: Config{AllowGroup: true, RequireMention: true}, chatID: "oc_chat", chatType: "group", lookupOK: true, botAuthored: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, captured := newThreadRoutingBot(t)
			b.cfg = tc.cfg
			b.resolveMessageContextFn = func(string) (string, string, string, bool, bool) {
				return tc.chatID, tc.chatType, "om_root", tc.botAuthored, tc.lookupOK
			}
			operatorType, openID, messageID, emoji := "user", "ou_sender", "om_reacted", "THUMBSUP"
			event := &larkim.P2MessageReactionCreatedV1{Event: &larkim.P2MessageReactionCreatedV1Data{
				OperatorType: &operatorType,
				UserId:       &larkim.UserId{OpenId: &openID},
				MessageId:    &messageID,
				ReactionType: &larkim.Emoji{EmojiType: &emoji},
			}}
			if err := b.onReaction(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if tc.want {
				msg := waitMessage(t, captured)
				if msg.ChatID != tc.chatID || !msg.IsGroup || msg.ThreadID != "om_root" {
					t.Fatalf("forwarded context = (%q, group=%v, %q)", msg.ChatID, msg.IsGroup, msg.ThreadID)
				}
			} else {
				assertNoMessage(t, captured)
			}
		})
	}
}

func TestCardActionIngressDeniedBeforeIdentityLookup(t *testing.T) {
	b, captured := newThreadRoutingBot(t)
	b.cfg = Config{AllowGroup: false, AllowDM: true}
	b.resolveMessageContextFn = func(string) (string, string, string, bool, bool) {
		return "oc_disallowed", "group", "", true, true
	}
	b.fetchTenantProfileFn = func(context.Context, string) *TenantProfile {
		t.Fatal("identity lookup occurred before ingress admission")
		return nil
	}
	response, err := b.onCardAction(context.Background(), &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_sender"},
		Action:   &callback.CallBackAction{Value: map[string]any{"action": "retry"}},
		Context:  &callback.Context{OpenChatID: "oc_forged", OpenMessageID: "om_card"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response != nil {
		t.Fatalf("denied action returned response: %#v", response)
	}
	assertNoMessage(t, captured)
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

func TestBotAuthoredMessageRequiresAuthoritativeSender(t *testing.T) {
	botAppID, app, user, openID, appID, otherAppID := "cli_bot", "app", "user", "open_id", "app_id", "cli_other"
	for _, tc := range []struct {
		name   string
		sender *larkim.Sender
		want   bool
	}{
		{name: "matching bot app id", sender: &larkim.Sender{Id: &botAppID, IdType: &appID, SenderType: &app}, want: true},
		{name: "user with matching id", sender: &larkim.Sender{Id: &botAppID, IdType: &appID, SenderType: &user}},
		{name: "bot open id is not app id", sender: &larkim.Sender{Id: &botAppID, IdType: &openID, SenderType: &app}},
		{name: "different app", sender: &larkim.Sender{Id: &otherAppID, IdType: &appID, SenderType: &app}},
		{name: "missing sender"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBotAuthoredMessage(&larkim.Message{Sender: tc.sender}, botAppID); got != tc.want {
				t.Fatalf("isBotAuthoredMessage() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAttachmentResolverErrorsFailClosed(t *testing.T) {
	for _, resolveErr := range []error{errors.New("resolver unavailable"), internalchannel.ErrAgentAccessDenied} {
		t.Run(resolveErr.Error(), func(t *testing.T) {
			b, h, captured := newThreadRoutingBotWithHandler(t)
			h.resolveUserRootFn = func(context.Context, channel.IncomingMessage) (string, error) {
				return "", resolveErr
			}
			event := receiveEvent("oc_chat", "group", "om_image", "", "", "image", `{"image_key":"img_secret"}`)
			if err := b.onMessage(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			assertNoMessage(t, captured)
		})
	}
}

func TestAttachmentRejectionText(t *testing.T) {
	for _, err := range []error{internalchannel.ErrAgentAccessDenied, agentaccess.ErrForbidden} {
		if got := attachmentRejectionText(err); got != "Guest chat currently supports text messages only." {
			t.Fatalf("attachmentRejectionText(%v) = %q", err, got)
		}
	}
	if got := attachmentRejectionText(errors.New("resolver unavailable")); got != "Unable to process this attachment right now." {
		t.Fatalf("generic attachment rejection = %q", got)
	}
}
