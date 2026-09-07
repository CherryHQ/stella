package agent

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/CherryHQ/stella/internal/plugin"
)

// ApplyPluginMutation keeps the database commit and runner retirement within
// one admission boundary. The callback must contain only a short transaction.
// Global admission fence; split by scope when plugin-write contention or
// tenant-wide runner churn becomes material.
func (pm *PoolManager) ApplyPluginMutation(ctx context.Context, mutate func() error) error {
	if pm == nil || pm.lifecycle == nil || mutate == nil {
		return errors.New("plugin mutation fence unavailable")
	}
	if err := pm.lifecycle.lockExclusive(ctx); err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			pm.lifecycle.unlockExclusive()
		}
	}()

	mutationErr := mutate()
	if mutationErr != nil && !errors.Is(mutationErr, plugin.ErrCommitOutcomeUnknown) {
		pm.lifecycle.unlockExclusive()
		locked = false
		return mutationErr
	}
	closers := pm.detachPluginRunnersLocked()
	pm.lifecycle.unlockExclusive()
	locked = false
	for _, closeRunners := range closers {
		if err := closeRunners(); err != nil {
			// Detach has already made the committed capability boundary visible.
			// Reporting a failed write here would misrepresent the commit.
			pm.log.Error("close retired plugin runners", "error", err)
		}
	}
	return mutationErr
}

// The caller holds lifecycle exclusively, so admissions and service publication
// cannot interleave. Detach marks idle runners retired and admitted runners
// stale; the returned closers run after lifecycle unlock so teardown cannot
// elongate the mutation critical section.
func (pm *PoolManager) detachPluginRunnersLocked() []func() error {
	pm.mu.RLock()
	services := slices.Collect(maps.Values(pm.services))
	pm.mu.RUnlock()
	closers := make([]func() error, 0, len(services))
	for _, svc := range services {
		closers = append(closers, svc.Runtime.DetachRunnersForMutation())
	}
	return closers
}
