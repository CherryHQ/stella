// Package toolctx provides identity extraction and ownership enforcement for
// native agent tools. Tools run in-process and read the acting agent/user/
// session from context.Context (set by the runner), so identity can never be
// supplied — or misstated — as a tool argument. The helpers here turn a missing
// required identity into an error and centralise the ownership check that every
// ID-based tool must perform, since the underlying service methods are not
// ownership-scoped (the HTTP handlers enforce ownership separately).
package toolctx

import (
	"context"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/internal/memory"
)

// ErrPermission is returned when the acting identity does not own the resource.
var ErrPermission = errors.New("permission denied")

// AgentID returns the acting agent ID, or an error if absent. Tools that act as
// the current agent require it; a missing agent means the runner did not bind
// one and the tool must not guess.
func AgentID(ctx context.Context) (string, error) {
	id := memory.AgentIDFromContext(ctx)
	if id == "" {
		return "", errors.New("no agent in context: this tool must run inside an agent session")
	}
	return id, nil
}

// UserID returns the acting user ID, or an error if absent.
func UserID(ctx context.Context) (string, error) {
	id := memory.UserIDFromContext(ctx)
	if id == "" {
		return "", errors.New("no user in context: this tool requires an authenticated user")
	}
	return id, nil
}

// SessionID returns the acting session ID, or an error if absent.
func SessionID(ctx context.Context) (string, error) {
	id := memory.SessionIDFromContext(ctx)
	if id == "" {
		return "", errors.New("no session in context: this tool requires an active session")
	}
	return id, nil
}

// RequireOwner rejects access unless the resource is owned by both the acting
// user and the acting agent. The agent check enforces the locked-agent model:
// one user may run multiple agents, and they must not cross-control each other's
// resources. A missing required identity in ctx is itself a rejection.
func RequireOwner(ctx context.Context, resourceUserID, resourceAgentID string) error {
	userID, err := UserID(ctx)
	if err != nil {
		return err
	}
	agentID, err := AgentID(ctx)
	if err != nil {
		return err
	}
	if resourceUserID != userID {
		return fmt.Errorf("%w: resource belongs to another user", ErrPermission)
	}
	if resourceAgentID != agentID {
		return fmt.Errorf("%w: resource belongs to another agent", ErrPermission)
	}
	return nil
}
