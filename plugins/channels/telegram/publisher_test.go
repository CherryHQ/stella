package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	tgmd "github.com/Mad-Pixels/goldmark-tgmd"
	tele "gopkg.in/telebot.v4"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

type telegramAPICall struct {
	method string
	params map[string]any
}

type telegramAPIFake struct {
	mu        sync.Mutex
	calls     []telegramAPICall
	responses map[string][]string
}

func (f *telegramAPIFake) RoundTrip(req *http.Request) (*http.Response, error) {
	var params map[string]any
	if err := json.NewDecoder(req.Body).Decode(&params); err != nil {
		return nil, err
	}
	method := path.Base(req.URL.Path)
	f.mu.Lock()
	f.calls = append(f.calls, telegramAPICall{method: method, params: params})
	response := `{"ok":true,"result":{"message_id":99,"chat":{"id":-100,"type":"supergroup"}}}`
	if queued := f.responses[method]; len(queued) > 0 {
		response = queued[0]
		f.responses[method] = queued[1:]
	}
	f.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(response)),
		Request:    req,
	}, nil
}

func (f *telegramAPIFake) callsFor(method string) []telegramAPICall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var calls []telegramAPICall
	for _, call := range f.calls {
		if call.method == method {
			calls = append(calls, call)
		}
	}
	return calls
}

func newPublisherTestBot(t *testing.T, fake *telegramAPIFake) *Bot {
	t.Helper()
	bot, err := tele.NewBot(tele.Settings{
		Offline:     true,
		Synchronous: true,
		Token:       "test",
		URL:         "https://telegram.invalid",
		Client:      &http.Client{Transport: fake},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Bot{bot: bot, md: tgmd.TGMD()}
}

func TestGroupPublisherRejectsLostRunBeforeAnyPlatformEffect(t *testing.T) {
	fake := &telegramAPIFake{}
	b := newPublisherTestBot(t, fake)
	events := make(chan channel.Event)
	close(events)
	want := errors.New("AgentRun ownership lost")
	err := b.Publish(context.Background(), internalchannel.GroupPublishRequest{
		PlatformGroupID: "-100",
		Stream: &channel.ChatStream{
			Events: events,
			OperationCheck: func(context.Context) error {
				return want
			},
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("Publish error = %v, want ownership loss", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("platform API calls after ownership loss = %#v", fake.calls)
	}
}

// The dispatcher hands over a complete turn, so the publisher sends exactly one
// message: no placeholder to edit, and the anchoring options ride on it.
func TestGroupPublisherSendsOneTopicMessage(t *testing.T) {
	fake := &telegramAPIFake{}
	b := newPublisherTestBot(t, fake)
	events := make(chan channel.Event, 4)
	stream := &channel.ChatStream{Events: events}
	go func() {
		events <- channel.Event{Text: "first"}
		events <- channel.Event{ToolUse: &channel.ToolUseEvent{Tool: "read", Status: "running", Input: "a.md"}}
		events <- channel.Event{Text: " second"}
		close(events)
	}()

	err := b.Publish(context.Background(), internalchannel.GroupPublishRequest{
		PlatformGroupID:  "-100",
		PlatformThreadID: "42",
		ReplyTo:          "7",
		Stream:           stream,
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	sends := fake.callsFor("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("sendMessage calls = %d, want one response message", len(sends))
	}
	if got := sends[0].params["message_thread_id"]; got != "42" {
		t.Fatalf("thread ID = %#v, want 42", got)
	}
	if got := sends[0].params["reply_to_message_id"]; got != "7" {
		t.Fatalf("reply anchor = %#v, want 7 (params %#v)", got, sends[0].params)
	}
	if got := sends[0].params["text"]; got == nil || !strings.Contains(got.(string), "first second") {
		t.Fatalf("text = %#v, want the complete response", got)
	}
	if edits := fake.callsFor("editMessageText"); len(edits) != 0 {
		t.Fatalf("editMessageText calls = %d, want none", len(edits))
	}
}

func TestGroupPublisherRejectsStreamFailureWithoutPlatformSideEffect(t *testing.T) {
	fake := &telegramAPIFake{}
	b := newPublisherTestBot(t, fake)
	events := make(chan channel.Event, 1)
	events <- channel.Event{Err: errors.New("secret upstream detail")}
	close(events)

	err := b.Publish(context.Background(), internalchannel.GroupPublishRequest{
		PlatformGroupID: "-100",
		Stream:          &channel.ChatStream{Events: events},
	})
	if err == nil {
		t.Fatal("Publish unexpectedly accepted a failed replay")
	}
	if calls := fake.callsFor("sendMessage"); len(calls) != 0 {
		t.Fatalf("sendMessage calls = %d, want no platform failure message", len(calls))
	}
}

func TestGroupPublisherReturnsFloodBeyondBound(t *testing.T) {
	fake := &telegramAPIFake{responses: map[string][]string{
		"sendMessage": {`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 6","parameters":{"retry_after":6}}`},
	}}
	b := newPublisherTestBot(t, fake)
	started := time.Now()
	err := b.Publish(context.Background(), internalchannel.GroupPublishRequest{PlatformGroupID: "-100"})
	if err == nil {
		t.Fatal("Publish accepted an exhausted flood error")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Publish waited %s for retry beyond bound", elapsed)
	}
	if got := len(fake.callsFor("sendMessage")); got != 1 {
		t.Fatalf("sendMessage calls = %d, want no retry beyond bound", got)
	}
}

func TestTelegramTreatsNoopEditAsSuccess(t *testing.T) {
	if !isTelegramNoopEdit(tele.ErrMessageNotModified) {
		t.Fatal("message-not-modified response was not recognized as success")
	}
}
