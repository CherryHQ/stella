package dingtalk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	streamclient "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"

	"github.com/CherryHQ/stella/pkg/channel"
)

type handledMessage struct {
	msg  channel.IncomingMessage
	cmd  string
	args string
}

type capturingHandler struct {
	messages chan handledMessage
}

func (h *capturingHandler) HandleIncoming(_ context.Context, msg channel.IncomingMessage, cmd, args string) (string, bool, *channel.ChatStream, error) {
	h.messages <- handledMessage{msg: msg, cmd: cmd, args: args}
	return "linked", true, nil, nil
}

func TestOnMessageNormalizesAndReplies(t *testing.T) {
	handler := &capturingHandler{messages: make(chan handledMessage, 1)}
	bot, err := New(Config{InstanceID: "ding-main", ClientID: "client", ClientSecret: "secret", AllowDM: true}, handler)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	replies := make(chan string, 1)
	bot.replyToWebhook = func(_ context.Context, webhook, text string) error {
		if webhook != "https://example.test/session" {
			t.Errorf("webhook = %q", webhook)
		}
		replies <- text
		return nil
	}

	_, err = bot.onMessage(context.Background(), &chatbot.BotCallbackDataModel{
		ConversationId:            "cid-private",
		ConversationType:          "1",
		SenderCorpId:              "corp-1",
		ChatbotCorpId:             "corp-1",
		MsgId:                     "msg-1",
		SenderStaffId:             "staff-1",
		SenderId:                  "encrypted-1",
		SenderNick:                "Alice",
		SessionWebhook:            "https://example.test/session",
		SessionWebhookExpiredTime: time.Now().Add(time.Hour).UnixMilli(),
		CreateAt:                  1_700_000_000_000,
		Text:                      chatbot.BotCallbackDataTextModel{Content: " /link ABC123 "},
	})
	if err != nil {
		t.Fatalf("onMessage: %v", err)
	}

	select {
	case got := <-handler.messages:
		if got.msg.Platform != channel.PlatformDingTalk || got.msg.ChannelID != "ding-main" || got.msg.ChatID != "cid-private" {
			t.Fatalf("routing fields = %#v", got.msg)
		}
		if got.msg.SenderID != "staff-1" || len(got.msg.SenderIDs) != 2 || got.msg.SenderIDs[1] != "encrypted-1" {
			t.Fatalf("sender fields = %#v", got.msg)
		}
		if got.cmd != "/link" || got.args != "ABC123" {
			t.Fatalf("command = %q %q", got.cmd, got.args)
		}
		if got.msg.Timestamp.IsZero() || got.msg.Timestamp.Location() != time.UTC {
			t.Fatalf("timestamp = %v", got.msg.Timestamp)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}

	select {
	case got := <-replies:
		if got != "linked" {
			t.Fatalf("reply = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("reply was not sent")
	}

	if err := bot.Notify(context.Background(), channel.Notification{ChatID: "staff-1", Text: "notice"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := <-replies; got != "notice" {
		t.Fatalf("notification = %q", got)
	}
}

func TestAdmissionIsFailClosedForGroups(t *testing.T) {
	tests := []struct {
		name       string
		allowGroup bool
		data       chatbot.BotCallbackDataModel
		want       bool
	}{
		{name: "allowed and mentioned", allowGroup: true, data: chatbot.BotCallbackDataModel{ConversationId: "cid-1", IsInAtList: true}, want: true},
		{name: "groups disabled", data: chatbot.BotCallbackDataModel{ConversationId: "cid-1", IsInAtList: true}},
		{name: "not mentioned", allowGroup: true, data: chatbot.BotCallbackDataModel{ConversationId: "cid-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot, err := New(Config{
				ClientID: "client", ClientSecret: "secret", AllowDM: true, RequireMention: true,
				AllowGroup: tt.allowGroup,
			}, &capturingHandler{messages: make(chan handledMessage, 1)})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := bot.admit(&tt.data, true); got != tt.want {
				t.Fatalf("admit = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDuplicateMessageIsIgnored(t *testing.T) {
	bot, err := New(Config{ClientID: "client", ClientSecret: "secret"}, &capturingHandler{messages: make(chan handledMessage, 1)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if bot.markSeen("msg-1") {
		t.Fatal("first delivery marked duplicate")
	}
	if !bot.markSeen("msg-1") {
		t.Fatal("second delivery was not marked duplicate")
	}
}

func TestStreamClientCloseDisablesSDKReconnect(t *testing.T) {
	client := streamclient.NewStreamClient()
	if !client.AutoReconnect {
		t.Fatal("SDK client unexpectedly starts with reconnect disabled")
	}
	wrapped := &dingTalkStreamClient{StreamClient: client}
	wrapped.Close()
	if client.AutoReconnect {
		t.Fatal("Close left SDK reconnect enabled")
	}
}

func TestExpiredSessionCannotNotify(t *testing.T) {
	bot, err := New(Config{ClientID: "client", ClientSecret: "secret"}, &capturingHandler{messages: make(chan handledMessage, 1)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bot.dmSessions["staff-1"] = webhookSession{URL: "https://example.test/session", ExpiresAt: time.Now().Add(-time.Minute)}
	if err := bot.Notify(context.Background(), channel.Notification{ChatID: "staff-1", Text: "notice"}); err == nil {
		t.Fatal("expected expired session error")
	}
}

func TestGroupSessionCannotReplaceDirectMessageSession(t *testing.T) {
	bot, err := New(Config{ClientID: "client", ClientSecret: "secret"}, &capturingHandler{messages: make(chan handledMessage, 1)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	expires := time.Now().Add(time.Hour).UnixMilli()
	bot.rememberSession(&chatbot.BotCallbackDataModel{
		ConversationId: "dm-1", SessionWebhook: "https://example.test/dm", SessionWebhookExpiredTime: expires,
	}, []string{"staff-1"}, false)
	bot.rememberSession(&chatbot.BotCallbackDataModel{
		ConversationId: "group-1", SessionWebhook: "https://example.test/group", SessionWebhookExpiredTime: expires,
	}, []string{"staff-1"}, true)

	dm, ok := bot.dmSessionFor("staff-1")
	if !ok || dm.URL != "https://example.test/dm" {
		t.Fatalf("DM session = %#v, %v", dm, ok)
	}
	group, ok := bot.groupSessionFor("group-1")
	if !ok || group.URL != "https://example.test/group" {
		t.Fatalf("group session = %#v, %v", group, ok)
	}
}

func TestSendWebhookTextRejectsDingTalkApplicationError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":130101,"errmsg":"send too fast"}`))
	}))
	defer server.Close()

	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	err := sendWebhookText(context.Background(), server.URL, "hello")
	if err == nil || !strings.Contains(err.Error(), "errcode=130101") {
		t.Fatalf("sendWebhookText error = %v", err)
	}
}

func TestOnMessageRejectsUnknownConversationAndExternalCorp(t *testing.T) {
	handler := &capturingHandler{messages: make(chan handledMessage, 1)}
	bot, err := New(Config{ClientID: "client", ClientSecret: "secret", AllowDM: true}, handler)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	base := chatbot.BotCallbackDataModel{
		ConversationId: "cid", MsgId: "msg", SenderStaffId: "staff", SessionWebhook: "https://example.test/session",
		SessionWebhookExpiredTime: time.Now().Add(time.Hour).UnixMilli(), Text: chatbot.BotCallbackDataTextModel{Content: "hello"},
		SenderCorpId: "corp-1", ChatbotCorpId: "corp-1",
	}
	unknown := base
	unknown.ConversationType = "unexpected"
	_, _ = bot.onMessage(context.Background(), &unknown)
	external := base
	external.MsgId = "msg-2"
	external.ConversationType = "1"
	external.SenderCorpId = "corp-2"
	_, _ = bot.onMessage(context.Background(), &external)
	select {
	case got := <-handler.messages:
		t.Fatalf("unexpected message: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCollectStreamReturnsTextAndError(t *testing.T) {
	events := make(chan channel.Event, 2)
	events <- channel.Event{Text: "partial"}
	events <- channel.Event{Err: errors.New("boom")}
	close(events)
	text, err := collectStream(context.Background(), &channel.ChatStream{Events: events})
	if text != "partial" || err == nil || err.Error() != "boom" {
		t.Fatalf("collectStream = %q, %v", text, err)
	}
}
