package discord

import (
	"context"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
)

type gatewayCursorStore struct {
	mu      sync.Mutex
	cursor  int64
	advance []int64
}

func (s *gatewayCursorStore) LoadIngressCursor(context.Context, string, string, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor, nil
}

func (s *gatewayCursorStore) AdvanceIngressCursor(_ context.Context, _ string, _ string, _ string, cursor int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cursor > s.cursor {
		s.cursor = cursor
	}
	s.advance = append(s.advance, cursor)
	return nil
}

func TestGatewayCursorAcksOnlyAfterAdmission(t *testing.T) {
	store := &gatewayCursorStore{}
	cursor := newGatewayCursor(store, "discord-main")
	if err := cursor.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	cursor.observe(&discordgo.Event{Operation: 0, Sequence: 11, Type: "MESSAGE_CREATE"})
	if process, tracked := cursor.begin("MESSAGE_CREATE"); !process || !tracked {
		t.Fatalf("begin() = process:%v tracked:%v, want true,true", process, tracked)
	}
	cursor.finish("MESSAGE_CREATE", false)
	store.mu.Lock()
	if store.cursor != 0 || len(store.advance) != 0 {
		store.mu.Unlock()
		t.Fatalf("failed admission advanced cursor: %#v", store)
	}
	store.mu.Unlock()

	cursor.finish("MESSAGE_CREATE", true)
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.cursor != 11 || len(store.advance) != 1 || store.advance[0] != 11 {
		t.Fatalf("successful admission cursor = %#v", store)
	}
}

func TestGatewayCursorKeepsSequenceOrderAcrossNonIngressEvents(t *testing.T) {
	store := &gatewayCursorStore{}
	cursor := newGatewayCursor(store, "discord-main")
	if err := cursor.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	cursor.observe(&discordgo.Event{Operation: 0, Sequence: 11, Type: "MESSAGE_CREATE"})
	cursor.observe(&discordgo.Event{Operation: 0, Sequence: 12, Type: "READY"})
	if process, tracked := cursor.begin("MESSAGE_CREATE"); !process || !tracked {
		t.Fatalf("begin() = process:%v tracked:%v", process, tracked)
	}
	cursor.finish("MESSAGE_CREATE", true)
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.cursor != 12 || len(store.advance) != 2 || store.advance[0] != 11 || store.advance[1] != 12 {
		t.Fatalf("cursor advances = %#v, cursor = %d", store.advance, store.cursor)
	}
}

func TestGatewayCursorSkipsPersistedSequence(t *testing.T) {
	store := &gatewayCursorStore{cursor: 20}
	cursor := newGatewayCursor(store, "discord-main")
	if err := cursor.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	cursor.observe(&discordgo.Event{Operation: 0, Sequence: 20, Type: "MESSAGE_CREATE"})
	if process, tracked := cursor.begin("MESSAGE_CREATE"); process || !tracked {
		t.Fatalf("begin() = process:%v tracked:%v, want false,true", process, tracked)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.advance) != 0 || store.cursor != 20 {
		t.Fatalf("duplicate sequence was advanced: %#v", store)
	}
}
