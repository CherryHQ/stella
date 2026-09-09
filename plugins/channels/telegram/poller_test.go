package telegram

import (
	"context"
	"errors"
	"sync"
	"testing"

	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/channel"
)

type pollerCursorStore struct {
	mu      sync.Mutex
	cursor  int64
	advance []int64
}

func (s *pollerCursorStore) LoadIngressCursor(context.Context, string, string, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor, nil
}

func (s *pollerCursorStore) AdvanceIngressCursor(_ context.Context, _ string, _ string, _ string, cursor int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cursor > s.cursor {
		s.cursor = cursor
	}
	s.advance = append(s.advance, cursor)
	return nil
}

func TestDurablePollerAdvancesAfterProcessUpdate(t *testing.T) {
	store := &pollerCursorStore{}
	poller := newDurablePoller(store, "telegram-main", 0, 0, nil)
	if err := poller.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	bot, err := tele.NewBot(tele.Settings{
		Token:       "token",
		Offline:     true,
		Synchronous: true,
		OnError:     poller.onError,
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	bot.Handle(tele.OnText, func(tele.Context) error {
		called = true
		return nil
	})
	if err := poller.process(context.Background(), bot, tele.Update{ID: 42, Message: &tele.Message{Text: "hello"}}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ProcessUpdate did not run the handler")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.cursor != 42 || len(store.advance) != 1 || store.advance[0] != 42 {
		t.Fatalf("cursor advances = %#v, cursor = %d", store.advance, store.cursor)
	}
}

func TestDurablePollerDoesNotAdvanceWhenHandlerFails(t *testing.T) {
	store := &pollerCursorStore{}
	poller := newDurablePoller(store, "telegram-main", 0, 0, nil)
	if err := poller.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	bot, err := tele.NewBot(tele.Settings{
		Token:       "token",
		Offline:     true,
		Synchronous: true,
		OnError:     poller.onError,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("admission unavailable")
	bot.Handle(tele.OnText, func(tele.Context) error { return wantErr })
	if err := poller.process(context.Background(), bot, tele.Update{ID: 42, Message: &tele.Message{Text: "hello"}}); !errors.Is(err, wantErr) {
		t.Fatalf("process error = %v, want %v", err, wantErr)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.cursor != 0 || len(store.advance) != 0 {
		t.Fatalf("failed admission advanced cursor: advances = %#v, cursor = %d", store.advance, store.cursor)
	}
}

func TestDurablePollerRequiresCursorStore(t *testing.T) {
	poller := newDurablePoller(nil, channel.PlatformTelegram, 0, 0, nil)
	if err := poller.load(context.Background()); err == nil {
		t.Fatal("load succeeded without a cursor store")
	}
}
