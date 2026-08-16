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

func TestGroupPublisherStreamsOneTopicProgressMessage(t *testing.T) {
	fake := &telegramAPIFake{}
	b := newPublisherTestBot(t, fake)
	events := make(chan channel.Event, 4)
	stream := &channel.ChatStream{Events: events}
	go func() {
		events <- channel.Event{Text: "first"}
		events <- channel.Event{ToolUse: &channel.ToolUseEvent{Tool: "read", Status: "running", Input: "a.md"}}
		time.Sleep(streamEditInterval + 50*time.Millisecond)
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
		t.Fatalf("sendMessage calls = %d, want one progress message", len(sends))
	}
	if got := sends[0].params["message_thread_id"]; got != "42" {
		t.Fatalf("progress thread ID = %#v, want 42", got)
	}
	if got := sends[0].params["reply_to_message_id"]; got != "7" {
		t.Fatalf("progress reply anchor = %#v, want 7 (params %#v)", got, sends[0].params)
	}
	edits := fake.callsFor("editMessageText")
	if len(edits) != 2 {
		t.Fatalf("editMessageText calls = %d, want coalesced progress plus final", len(edits))
	}
	if got := edits[len(edits)-1].params["text"]; got == nil || !strings.Contains(got.(string), "first second") {
		t.Fatalf("final edit text = %#v, want complete response", got)
	}
	if typing := fake.callsFor("sendChatAction"); len(typing) == 0 {
		t.Fatal("group publisher did not send typing")
	}
}

func TestGroupPublisherMakesStreamFailureVisibleWithoutLeakingError(t *testing.T) {
	fake := &telegramAPIFake{}
	b := newPublisherTestBot(t, fake)
	events := make(chan channel.Event, 1)
	events <- channel.Event{Err: errors.New("secret upstream detail")}
	close(events)

	err := b.Publish(context.Background(), internalchannel.GroupPublishRequest{
		PlatformGroupID: "-100",
		Stream:          &channel.ChatStream{Events: events},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	edits := fake.callsFor("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("editMessageText calls = %d, want final failure edit", len(edits))
	}
	text, _ := edits[0].params["text"].(string)
	if !strings.Contains(text, "could not be completed") || strings.Contains(text, "secret upstream detail") {
		t.Fatalf("failure text = %q", text)
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

func TestRetryTelegramTreatsNoopEditAsSuccess(t *testing.T) {
	if err := retryTelegram(context.Background(), func() error { return tele.ErrMessageNotModified }); err != nil {
		t.Fatalf("retryTelegram(noop): %v", err)
	}
}
