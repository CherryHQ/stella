// Package tasks is the home of the v2 task system.
//
// PHASE 1 STUB — see plan.md (D14 / MP1): the schema has been rewritten but the
// Go-side transition service, dispatcher, worker, and control tool have not
// been ported yet. Every public method returns ErrNotImplemented, the
// dispatcher does not start, and HTTP handlers fall back to 503. Server boot
// still succeeds so the rest of the system runs.
package tasks

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ErrNotImplemented is returned by every Service method while the v2 task
// system is being ported. Handlers map this to HTTP 503.
var ErrNotImplemented = errors.New("task system v2 not yet initialized")

// RunnerFactoryFn resolves a runner factory for a given agent ID.
// Retained for source-compat with cmd/stella/commands.go; not yet consulted.
type RunnerFactoryFn func(agentID string) (agent.NewRunnerFunc, bool)

// Service is the public handle exposed to the server and the boot wiring.
// In Phase 1 it carries no state beyond a logger.
type Service struct {
	log *slog.Logger
}

// Config preserves the construction signature so cmd/stella/commands.go
// continues to compile. Fields are accepted and ignored.
type Config struct {
	Queries        *sqlc.Queries
	Notifier       notify.Notifier
	Memory         memory.Provider
	RunnerFactory  RunnerFactoryFn
	MaxConcurrency int
}

// CreateTaskParams is the input shape for CreateTask. Preserved for source
// compatibility; Phase 5/6 will replace it with a typed request body.
type CreateTaskParams struct {
	Title       string
	Description string
	Priority    string
	AgentID     string
	UserID      string
	OrgID       string
	Deps        []string
}

// UpdateTaskParams holds parameters for updating task metadata.
type UpdateTaskParams struct {
	Title       string
	Description string
	Priority    string
	AgentID     string
}

// ActionParams holds parameters for the legacy /action endpoint.
type ActionParams struct {
	Action  string
	Message string
}

// New constructs a Service. The returned Service is intentionally inert until
// Phase 2 lands the transition service.
func New(_ Config) *Service {
	return &Service{log: slog.New(slog.NewTextHandler(os.Stderr, nil)).With("component", "tasks/stub")}
}

// Start is a no-op; the dispatcher is not yet ported.
func (s *Service) Start(_ context.Context) error { return nil }

// Stop is a no-op.
func (s *Service) Stop() {}

// Tick is a no-op; the dispatcher loop is not yet ported.
func (s *Service) Tick() {}

// CreateTask returns ErrNotImplemented. Phase 5/6 will replace this.
func (s *Service) CreateTask(_ context.Context, _ CreateTaskParams) (sqlc.AgentTask, error) {
	return sqlc.AgentTask{}, ErrNotImplemented
}

// GetTask returns ErrNotImplemented.
func (s *Service) GetTask(_ context.Context, _, _ string) (sqlc.AgentTask, error) {
	return sqlc.AgentTask{}, ErrNotImplemented
}

// ListTasks returns an empty slice with ErrNotImplemented.
func (s *Service) ListTasks(_ context.Context, _, _, _ string) ([]sqlc.AgentTask, error) {
	return nil, ErrNotImplemented
}

// UpdateTask returns ErrNotImplemented.
func (s *Service) UpdateTask(_ context.Context, _, _ string, _ UpdateTaskParams) (sqlc.AgentTask, error) {
	return sqlc.AgentTask{}, ErrNotImplemented
}

// DeleteTask returns ErrNotImplemented.
func (s *Service) DeleteTask(_ context.Context, _, _ string) error {
	return ErrNotImplemented
}

// HandleAction returns ErrNotImplemented.
func (s *Service) HandleAction(_ context.Context, _, _ string, _ ActionParams) (sqlc.AgentTask, error) {
	return sqlc.AgentTask{}, ErrNotImplemented
}

// ListTaskEvents returns an empty slice with ErrNotImplemented.
func (s *Service) ListTaskEvents(_ context.Context, _ string) ([]sqlc.AgentTaskEvent, error) {
	return nil, ErrNotImplemented
}
