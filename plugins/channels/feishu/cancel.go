package feishu

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/channel"
)

const (
	cancelCardActionPrefix = "stella.cancel:"
	// cancelRegistryLimit is global per bot instance; raise it only if one bot
	// routinely has more concurrent turns than this. Per-chat tracking is not
	// needed until that throughput becomes real.
	cancelRegistryLimit = 512
)

type cancelControl struct {
	requesterID string
	abort       func() bool
	cancelled   atomic.Bool
}

func (c *cancelControl) wasCancelled() bool {
	return c != nil && c.cancelled.Load()
}

// newDirectCancelControl re-enters the coordinator's existing /abort command
// path. The plugin holds no second session queue or turn lifecycle.
func (b *Bot) newDirectCancelControl(ctx context.Context, msg channel.IncomingMessage, senderID string) *cancelControl {
	if b == nil || b.handler == nil || senderID == "" {
		return nil
	}
	return &cancelControl{requesterID: senderID, abort: func() bool {
		resp, handled, _, err := b.handler.HandleIncoming(context.WithoutCancel(ctx), msg, "/abort", "")
		return err == nil && handled && strings.TrimSpace(resp) == "Aborted."
	}}
}

type cancelEntry struct {
	requesterID string
	chatID      string
	rootID      string
	control     *cancelControl
}

// cancelRegistry holds only in-flight, process-local cancel actions. A restart
// intentionally makes old cards inert; delivery remains at-least-once.
type cancelRegistry struct {
	mu      sync.Mutex
	entries map[string]cancelEntry
	order   []string
}

func newCancelRegistry() *cancelRegistry {
	return &cancelRegistry{entries: make(map[string]cancelEntry)}
}

func (r *cancelRegistry) register(requesterID, chatID, rootID string, control *cancelControl) string {
	if r == nil || requesterID == "" || control == nil || control.abort == nil {
		return ""
	}
	token := uuid.Must(uuid.NewV7()).String()
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.entries) >= cancelRegistryLimit && len(r.order) > 0 {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.entries, oldest)
	}
	r.entries[token] = cancelEntry{requesterID: requesterID, chatID: chatID, rootID: rootID, control: control}
	r.order = append(r.order, token)
	return token
}

func (r *cancelRegistry) get(token string) (cancelEntry, bool) {
	if r == nil || token == "" {
		return cancelEntry{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[token]
	return entry, ok
}

// take removes and returns one entry. Removing before Abort guarantees that
// concurrent callback deliveries invoke the coordinator's cancellation at most once.
func (r *cancelRegistry) take(token string) (cancelEntry, bool) {
	if r == nil || token == "" {
		return cancelEntry{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[token]
	if !ok {
		return cancelEntry{}, false
	}
	delete(r.entries, token)
	if idx := slices.Index(r.order, token); idx >= 0 {
		r.order = slices.Delete(r.order, idx, idx+1)
	}
	return entry, true
}

func (r *cancelRegistry) unregister(token string) {
	_, _ = r.take(token)
}

func cancelActionToken(action string) string {
	return strings.TrimPrefix(action, cancelCardActionPrefix)
}

func (b *Bot) unregisterCancel(token string) {
	if b != nil && b.cancels != nil {
		b.cancels.unregister(token)
	}
}

func cancelCardText(text, token string) string {
	if token == "" {
		return text
	}
	return text + "\n\n{{button value=\"" + cancelCardActionPrefix + token + "\" type=\"danger\" label=\"Cancel\"}}"
}
