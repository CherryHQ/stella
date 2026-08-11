package agent

import (
	"context"
	"sync"
)

// contextMutex is a context-aware binary semaphore. Its zero value is ready
// for use; initialization is guarded so callers never need helper goroutines.
type contextMutex struct {
	once sync.Once
	ch   chan struct{}
}

func (m *contextMutex) init() {
	m.once.Do(func() {
		m.ch = make(chan struct{}, 1)
		m.ch <- struct{}{}
	})
}

func (m *contextMutex) Lock(ctx context.Context) error {
	m.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ch:
		return nil
	}
}

func (m *contextMutex) Unlock() {
	m.init()
	m.ch <- struct{}{}
}
