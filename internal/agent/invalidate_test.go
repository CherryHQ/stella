package agent

import (
	"context"
	"testing"
)

// TestResetRunnersForUser verifies that ResetRunnersForUser closes runners
// only for the targeted user, leaving other users' runners intact.
func TestResetRunnersForUser(t *testing.T) {
	const user1 = "1"
	const user2 = "2"

	ctx := context.Background()
	factory, runners := mockRunnerFactory(nil)
	pool := NewPool(factory, testMemoryProvider(t))
	defer func() { _ = pool.Close() }()

	// Create a session per user by calling getOrCreateRunner.
	_, _, err := pool.getOrCreateRunner(ctx, "sess-u1", "")
	if err != nil {
		t.Fatalf("getOrCreateRunner user1: %v", err)
	}
	_, _, err = pool.getOrCreateRunner(ctx, "sess-u2", "")
	if err != nil {
		t.Fatalf("getOrCreateRunner user2: %v", err)
	}

	// Wire user IDs directly on the sessions (pool_runner.go normally does this
	// from the memory provider, but skipping memory for this unit test).
	pool.mu.Lock()
	if s := pool.sessions["sess-u1"]; s != nil {
		s.Info.UserID = user1
	}
	if s := pool.sessions["sess-u2"]; s != nil {
		s.Info.UserID = user2
	}
	pool.mu.Unlock()

	if len(*runners) != 2 {
		t.Fatalf("expected 2 runners created, got %d", len(*runners))
	}

	// Invalidate only user1.
	if err := pool.ResetRunnersForUser(user1); err != nil {
		t.Fatalf("ResetRunnersForUser: %v", err)
	}

	// Exactly one runner should be closed.
	closedCount := 0
	for _, r := range *runners {
		r.mu.Lock()
		if r.closed {
			closedCount++
		}
		r.mu.Unlock()
	}
	if closedCount != 1 {
		t.Errorf("expected exactly 1 runner closed after ResetRunnersForUser, got %d", closedCount)
	}
}

// TestInvalidateUserAcrossPools verifies PoolManager.InvalidateUser propagates
// to all registered pools.
func TestInvalidateUserAcrossPools(t *testing.T) {
	const userID = "7"

	ctx := context.Background()
	factory1, runners1 := mockRunnerFactory(nil)
	factory2, runners2 := mockRunnerFactory(nil)

	p1 := NewPool(factory1, testMemoryProvider(t))
	defer func() { _ = p1.Close() }()
	p2 := NewPool(factory2, testMemoryProvider(t))
	defer func() { _ = p2.Close() }()

	pm := NewPoolManager(nil, nil)
	pm.pools = map[string]*Pool{"agent-a": p1, "agent-b": p2}

	// Create sessions with the target userID in each pool.
	for i, p := range []*Pool{p1, p2} {
		sessID := "sess-" + string(rune('1'+i))
		if _, _, err := p.getOrCreateRunner(ctx, sessID, ""); err != nil {
			t.Fatalf("getOrCreateRunner pool%d: %v", i, err)
		}
		p.mu.Lock()
		if s := p.sessions[sessID]; s != nil {
			s.Info.UserID = userID
		}
		p.mu.Unlock()
	}

	if err := pm.InvalidateUser(userID); err != nil {
		t.Fatalf("InvalidateUser: %v", err)
	}

	// Both runners should be closed.
	for i, runners := range []*[]*mockRunner{runners1, runners2} {
		for _, r := range *runners {
			r.mu.Lock()
			closed := r.closed
			r.mu.Unlock()
			if !closed {
				t.Errorf("runner in pool %d should have been closed by InvalidateUser", i+1)
			}
		}
	}
}
