package agent

import (
	"context"

	"golang.org/x/sync/semaphore"
)

const lifecycleGateCapacity int64 = 1 << 20

// lifecycleGate serializes process-local PoolManager lifecycle mutation with
// runner admission. Weighted queues an exclusive waiter ahead of later readers,
// so a steady admission stream cannot starve lifecycle work.
//
// Callers must not upgrade shared ownership to exclusive ownership or acquire
// either mode reentrantly. The gate deliberately does not track owners.
type lifecycleGate struct {
	sem *semaphore.Weighted
}

func newLifecycleGate() *lifecycleGate {
	return &lifecycleGate{sem: semaphore.NewWeighted(lifecycleGateCapacity)}
}

func (g *lifecycleGate) lockShared(ctx context.Context) error {
	return g.sem.Acquire(ctx, 1)
}

func (g *lifecycleGate) unlockShared() {
	g.sem.Release(1)
}

func (g *lifecycleGate) lockExclusive(ctx context.Context) error {
	return g.sem.Acquire(ctx, lifecycleGateCapacity)
}

func (g *lifecycleGate) unlockExclusive() {
	g.sem.Release(lifecycleGateCapacity)
}
