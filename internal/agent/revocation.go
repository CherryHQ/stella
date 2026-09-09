package agent

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
)

// ApplyUserRevocation runs a destructive user, user-Agent, system-Agent, or
// deployment-wide mutation inside the same short lifecycle fence used by
// plugin writes. Empty userID selects all users, and empty agentID selects all
// agents, so the empty/empty pair is deployment-wide. A successful mutation,
// and any error that leaves its durable outcome unknown, gets a terminal
// runner cutoff before the fence is released. A known rollback leaves the
// existing cache untouched so a retried write does not poison live sessions.
// Runner Close is deliberately outside the fence; a close failure is logged
// and retried by Runtime's retired-owner path, never reported as a write error.
func (pm *PoolManager) ApplyUserRevocation(ctx context.Context, userID, agentID string, mutate func() error) error {
	// Empty identifiers are intentional scope selectors: empty/empty means the
	// whole deployment, while empty user plus an agent targets every user on
	// that agent (system_agent). The caller has already authorized that scope.
	if pm == nil || pm.lifecycle == nil || mutate == nil {
		return errors.New("user revocation coordinator unavailable")
	}
	if ctx == nil {
		return errors.New("user revocation context is nil")
	}
	if err := pm.lifecycle.lockExclusive(ctx); err != nil {
		return err
	}

	mutationErr := mutate()
	terminal := mutationErr == nil || userRevocationOutcomeUnknown(mutationErr)
	var closers []func() error
	if terminal {
		closers = pm.detachUserRunnersLocked(userID, agentID)
	}
	pm.lifecycle.unlockExclusive()

	if terminal {
		for _, closeRunner := range closers {
			if err := closeRunner(); err != nil {
				pm.log.Error("close revoked user runners", "user_id", userID, "agent_id", agentID, "error", err)
			}
		}
	}
	return mutationErr
}

func userRevocationOutcomeUnknown(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, plugin.ErrCommitOutcomeUnknown) ||
		errors.Is(err, home.ErrOutcomeUnknown) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		isNetworkOutcomeUnknown(err)
}

// A transport failure can happen after PostgreSQL or the vault accepted the
// write but before the caller received its acknowledgement. Keep this list
// deliberately conservative: server-side constraint errors are known
// rollbacks, while a broken transport is an unknown commit outcome.
func isNetworkOutcomeUnknown(err error) bool {
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

// detachUserRunnersLocked performs only the cache ownership transition. The
// caller holds lifecycle exclusively; returned closures must run after that
// lock is released.
func (pm *PoolManager) detachUserRunnersLocked(userID, agentID string) []func() error {
	pm.mu.RLock()
	services := make([]*Service, 0, len(pm.services))
	if agentID != "" {
		if svc := pm.services[agentID]; svc != nil {
			services = append(services, svc)
		}
	} else {
		for _, svc := range pm.services {
			services = append(services, svc)
		}
	}
	pm.mu.RUnlock()

	closers := make([]func() error, 0, len(services))
	for _, svc := range services {
		_ = svc.admissionMu.Lock(context.Background())
		closers = append(closers, svc.Runtime.DetachRunnersWhere(func(info session.Info) bool {
			return (userID == "" || info.UserID == userID) &&
				(agentID == "" || info.AgentID == agentID)
		}))
		svc.admissionMu.Unlock()
	}
	return closers
}
