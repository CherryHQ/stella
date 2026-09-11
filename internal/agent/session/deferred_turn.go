package session

import (
	"context"
	"sync"

	"github.com/CherryHQ/stella/pkg/ai"
)

// DeferredTurnStore collects a direct turn's durable messages instead of
// appending them to memory mid-run. The run worker installs one so the final
// assistant history joins the run/outbox finish transaction — the turn is
// either fully visible or not at all (plan D4).
type DeferredTurnStore struct {
	mu   sync.Mutex
	rows []ai.Message
}

func (s *DeferredTurnStore) Append(msgs ...ai.Message) {
	s.mu.Lock()
	s.rows = append(s.rows, msgs...)
	s.mu.Unlock()
}

// Rows returns the collected messages in append order.
func (s *DeferredTurnStore) Rows() []ai.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ai.Message(nil), s.rows...)
}

type deferredTurnKey struct{}

// WithDeferredTurnStore defers a turn's durable appends into store.
func WithDeferredTurnStore(ctx context.Context, store *DeferredTurnStore) context.Context {
	return context.WithValue(ctx, deferredTurnKey{}, store)
}

func DeferredTurnStoreFrom(ctx context.Context) (*DeferredTurnStore, bool) {
	s, ok := ctx.Value(deferredTurnKey{}).(*DeferredTurnStore)
	return s, ok && s != nil
}
