package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"

	tgmd "github.com/Mad-Pixels/goldmark-tgmd"
	tele "gopkg.in/telebot.v4"
)

type telegramAPICall struct {
	method string
	params map[string]any
}

type telegramAPIFake struct {
	mu        sync.Mutex
	calls     []telegramAPICall
	responses map[string][]string
	// onCall runs after each call is recorded — tests use it to flip external
	// state (e.g. the channel lease) between two SDK calls of one op.
	onCall func(method string)
}

func (f *telegramAPIFake) RoundTrip(req *http.Request) (*http.Response, error) {
	var params map[string]any
	if strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/form-data") {
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			return nil, err
		}
		params = make(map[string]any, len(req.MultipartForm.Value)+len(req.MultipartForm.File))
		for k, v := range req.MultipartForm.Value {
			params[k] = strings.Join(v, ",")
		}
		for k := range req.MultipartForm.File {
			params[k] = "<upload>"
		}
	} else if err := json.NewDecoder(req.Body).Decode(&params); err != nil {
		return nil, err
	}
	method := path.Base(req.URL.Path)
	f.mu.Lock()
	f.calls = append(f.calls, telegramAPICall{method: method, params: params})
	if f.onCall != nil {
		f.onCall(method)
	}
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

func TestRetryTelegramTreatsNoopEditAsSuccess(t *testing.T) {
	if err := retryTelegram(context.Background(), func() error { return tele.ErrMessageNotModified }); err != nil {
		t.Fatalf("retryTelegram(noop): %v", err)
	}
}
