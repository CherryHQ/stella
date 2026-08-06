package groupingest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
)

type fakeGroupFactStore struct {
	mu      sync.RWMutex
	version int64
	facts   []memory.GroupFact
	names   []memory.GroupActorDisplayName
	err     error
	delay   time.Duration

	versionCalls atomic.Int32
	factCalls    atomic.Int32
	nameCalls    atomic.Int32
}

func (s *fakeGroupFactStore) GetGroupFactVersion(context.Context, string) (int64, error) {
	s.versionCalls.Add(1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version, s.err
}

func (s *fakeGroupFactStore) ListActiveGroupFacts(context.Context, string) ([]memory.GroupFact, error) {
	s.factCalls.Add(1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]memory.GroupFact(nil), s.facts...), s.err
}

func (s *fakeGroupFactStore) ListGroupActorDisplayNames(context.Context, string) ([]memory.GroupActorDisplayName, error) {
	s.nameCalls.Add(1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]memory.GroupActorDisplayName(nil), s.names...), s.err
}

func TestGroupFactCacheChecksVersionOnlyAfterTTL(t *testing.T) {
	now := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	store := &fakeGroupFactStore{
		version: 1,
		facts: []memory.GroupFact{{
			Subject:   memory.GroupFactSubjectHuman,
			SubjectID: "alice",
			Content:   "Owns production release coordination.",
		}},
		names: []memory.GroupActorDisplayName{{
			Subject:     memory.GroupFactSubjectHuman,
			SubjectID:   "alice",
			DisplayName: "Alice",
		}},
	}
	cache, err := NewGroupFactCache(store, GroupFactCacheOptions{
		TTL: time.Hour,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	first, err := cache.GetPromptBlock(context.Background(), "group-1")
	if err != nil {
		t.Fatalf("cold load: %v", err)
	}
	if !strings.Contains(first, `"display_name":"Alice"`) {
		t.Fatalf("rendered block missing display name: %s", first)
	}
	if _, err := cache.GetPromptBlock(context.Background(), "group-1"); err != nil {
		t.Fatalf("cache hit: %v", err)
	}
	if store.versionCalls.Load() != 1 || store.factCalls.Load() != 1 {
		t.Fatalf("cold+hit calls version=%d facts=%d", store.versionCalls.Load(), store.factCalls.Load())
	}

	now = now.Add(2 * time.Hour)
	if _, err := cache.GetPromptBlock(context.Background(), "group-1"); err != nil {
		t.Fatalf("version-only refresh: %v", err)
	}
	if store.versionCalls.Load() != 2 || store.factCalls.Load() != 1 {
		t.Fatalf("same-version calls version=%d facts=%d", store.versionCalls.Load(), store.factCalls.Load())
	}

	store.mu.Lock()
	store.version = 2
	store.facts[0].Content = "Owns all production deployments."
	store.mu.Unlock()
	now = now.Add(2 * time.Hour)
	updated, err := cache.GetPromptBlock(context.Background(), "group-1")
	if err != nil {
		t.Fatalf("changed-version refresh: %v", err)
	}
	if !strings.Contains(updated, "Owns all production deployments.") {
		t.Fatalf("updated block = %s", updated)
	}
	if store.factCalls.Load() != 2 || store.nameCalls.Load() != 2 {
		t.Fatalf("reload calls facts=%d names=%d", store.factCalls.Load(), store.nameCalls.Load())
	}
}

func TestGroupFactCacheSharesColdLoadAcrossAgents(t *testing.T) {
	store := &fakeGroupFactStore{
		version: 1,
		delay:   20 * time.Millisecond,
		facts: []memory.GroupFact{{
			Subject: memory.GroupFactSubjectGroup,
			Content: "Use staging before production.",
		}},
	}
	cache, err := NewGroupFactCache(store, GroupFactCacheOptions{})
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			_, getErr := cache.GetPromptBlock(context.Background(), "group-1")
			errs <- getErr
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("shared cold load: %v", err)
		}
	}
	if store.versionCalls.Load() != 1 || store.factCalls.Load() != 1 || store.nameCalls.Load() != 1 {
		t.Fatalf("calls version=%d facts=%d names=%d",
			store.versionCalls.Load(), store.factCalls.Load(), store.nameCalls.Load())
	}
}

func TestGroupFactCacheUsesWarmStaleValueButColdMissFails(t *testing.T) {
	now := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	store := &fakeGroupFactStore{
		version: 1,
		facts: []memory.GroupFact{{
			Subject: memory.GroupFactSubjectGroup,
			Content: "Use staging before production.",
		}},
	}
	cache, err := NewGroupFactCache(store, GroupFactCacheOptions{
		TTL: time.Hour,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}
	first, err := cache.GetPromptBlock(context.Background(), "group-1")
	if err != nil {
		t.Fatalf("cold load: %v", err)
	}

	store.mu.Lock()
	store.err = errors.New("database unavailable")
	store.mu.Unlock()
	now = now.Add(2 * time.Hour)
	stale, err := cache.GetPromptBlock(context.Background(), "group-1")
	if err != nil {
		t.Fatalf("warm stale fallback: %v", err)
	}
	if stale != first {
		t.Fatalf("stale block changed")
	}
	versionCallsAfterFailure := store.versionCalls.Load()
	if _, err := cache.GetPromptBlock(context.Background(), "group-1"); err != nil {
		t.Fatalf("stale retry backoff hit: %v", err)
	}
	if store.versionCalls.Load() != versionCallsAfterFailure {
		t.Fatal("warm stale cache retried before the failure backoff elapsed")
	}

	store.mu.Lock()
	store.err = nil
	store.version = 2
	store.facts[0].Content = "Use the recovered production database."
	store.mu.Unlock()
	now = now.Add(groupFactCacheRetryBackoff)
	recovered, err := cache.GetPromptBlock(context.Background(), "group-1")
	if err != nil {
		t.Fatalf("refresh after stale retry backoff: %v", err)
	}
	if !strings.Contains(recovered, "Use the recovered production database.") {
		t.Fatalf("recovered block = %s", recovered)
	}

	store.mu.Lock()
	store.err = errors.New("database unavailable")
	store.mu.Unlock()
	cold, err := NewGroupFactCache(store, GroupFactCacheOptions{})
	if err != nil {
		t.Fatalf("new cold cache: %v", err)
	}
	if _, err := cold.GetPromptBlock(context.Background(), "group-2"); err == nil {
		t.Fatal("cold miss should fail when the store is unavailable")
	}
}
