package discord

import (
	"slices"
	"sync"

	"github.com/google/uuid"
)

// cancelCustomIDPrefix routes a message-component interaction to the cancel
// handler. The rest of the custom_id is an opaque registry token: no
// requester, channel, or message identity is ever embedded in it, so it
// leaks nothing to whoever can see the button and grants nothing on its own —
// authorization comes from matching the clicking user against the registry
// entry, not from anything encoded in the ID itself.
const cancelCustomIDPrefix = "cxl_"

// cancelRegistryLimit bounds the in-memory registry so a burst of concurrent
// drafts cannot grow it without bound; global cap, not per-user — raise if a
// single-bot deployment routinely runs more concurrent turns than this.
const cancelRegistryLimit = 512

// cancelEntry is one outstanding cancel button's authorization and effect.
type cancelEntry struct {
	requesterID string
	abort       func() bool
}

// cancelRegistry maps opaque custom_id tokens to the cancel entry they
// authorize. It is process-local, in-memory, and unordered beyond FIFO
// eviction: a restart drops every entry, which is why an unknown token must
// be treated as an ended, not denied, action (see handleComponentInteraction).
type cancelRegistry struct {
	mu      sync.Mutex
	entries map[string]cancelEntry
	order   []string
}

func newCancelRegistry() *cancelRegistry {
	return &cancelRegistry{entries: make(map[string]cancelEntry)}
}

// register stores entry and returns its opaque token. abort must be non-nil;
// callers that have no abort capability should skip registration entirely
// (see beginDraft) rather than registering a no-op.
func (r *cancelRegistry) register(requesterID string, abort func() bool) string {
	token := uuid.Must(uuid.NewV7()).String()
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.entries) >= cancelRegistryLimit && len(r.order) > 0 {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.entries, oldest)
	}
	r.entries[token] = cancelEntry{requesterID: requesterID, abort: abort}
	r.order = append(r.order, token)
	return token
}

// unregister removes token, if present, from both entries and order. Safe to
// call more than once and with an empty token. Cleaning order here — not just
// entries — matters: most tokens resolve (a turn finishes or is cancelled)
// long before the registry ever reaches cancelRegistryLimit, so without this
// order would grow without bound even though entries stays small, defeating
// the point of a bounded registry.
func (r *cancelRegistry) unregister(token string) {
	if r == nil || token == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[token]; !ok {
		return
	}
	delete(r.entries, token)
	if idx := slices.Index(r.order, token); idx >= 0 {
		r.order = slices.Delete(r.order, idx, idx+1)
	}
}

// get returns the entry for token, if it is still registered.
func (r *cancelRegistry) get(token string) (cancelEntry, bool) {
	if r == nil || token == "" {
		return cancelEntry{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[token]
	return e, ok
}

// cancelControl is what a deliverStream caller supplies to attach a Cancel
// button to the draft it creates. A nil abort means "no button" — deliverStream
// treats a nil *cancelControl and a non-nil one with a nil abort the same way.
type cancelControl struct {
	requesterID string
	abort       func() bool
}

// unregisterCancel is a nil-safe helper so draft cleanup does not need to
// guard on both the Bot and its registry being non-nil at every call site.
func (b *Bot) unregisterCancel(token string) {
	if b == nil || b.cancels == nil {
		return
	}
	b.cancels.unregister(token)
}
