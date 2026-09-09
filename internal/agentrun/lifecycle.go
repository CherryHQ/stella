package agentrun

import (
	"context"
	"fmt"
	"time"
)

// RegisterBoot runs once during process setup, before any admission is exposed.
func (s *Store) RegisterBoot(ctx context.Context) error {
	if s == nil || s.db == nil || s.executorBootID == "" {
		return fmt.Errorf("AgentRun store is not configured")
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if err := s.requireAdmission(); err != nil {
		return err
	}
	bootCtx, release := s.bindLifecycle(ctx)
	defer release()
	if _, err := s.q.CreateExecutorBoot(bootCtx, s.executorBootID); err != nil {
		return fmt.Errorf("register executor boot: %w", err)
	}
	s.bootRegistered.Store(true)
	s.bootDrained.Store(false)
	return nil
}

// DrainBoot records the process shutdown after ingress and accepted work drain.
// A crashed process leaves its Runs to expire; a boot is never reused on restart.
func (s *Store) DrainBoot(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if !s.bootRegistered.Load() {
		return ErrBootUnavailable
	}
	// Drain is the final durable shutdown marker. It must remain possible after
	// Close has canceled lifecycle workers, so use only the caller's bounded
	// context and keep the admission lock to serialize against a commit already
	// in flight. This cannot reactivate admission: closed/bootDrained stay set.
	rows, err := s.q.DrainExecutorBoot(ctx, s.executorBootID)
	if err != nil {
		return fmt.Errorf("drain executor boot: %w", err)
	}
	if rows != 1 {
		return ErrBootUnavailable
	}
	s.bootDrained.Store(true)
	return nil
}

// Run is the single process-wide reconciliation loop. Maintenance failure
// stops serving instead of leaving recovery silently disabled.
func (s *Store) Run(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		stepCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		rows, err := s.q.HeartbeatExecutorBoot(stepCtx, s.executorBootID)
		if err == nil && rows != 1 {
			err = ErrLeaseLost
		}
		if err == nil {
			err = s.Reap(stepCtx)
		}
		cancel()
		if err != nil {
			return fmt.Errorf("reconcile executor boot: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
