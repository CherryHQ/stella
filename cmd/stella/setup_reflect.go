package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	reflectplugin "github.com/CherryHQ/stella/internal/reflect"
	"github.com/CherryHQ/stella/internal/scheduler"
)

const (
	defaultReflectInterval = time.Hour
	reflectBuiltinJobName  = "reflect-review"
)

// registerReflectBuiltin wires the reflect review cycle into the scheduler
// as a per-org builtin job. The cadence defaults to 1h and can be overridden
// for development via the STELLA_REFLECT_INTERVAL env var (Go duration
// string). The override is intentionally undocumented in user-facing docs
// — it exists so verifying reflect's wiring doesn't require a code rebuild.
func registerReflectBuiltin(svc *scheduler.Service, deps reflectplugin.DispatcherDeps) error {
	every := defaultReflectInterval
	if raw := os.Getenv("STELLA_REFLECT_INTERVAL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			every = parsed
		} else {
			slog.Warn("reflect: invalid STELLA_REFLECT_INTERVAL, using default",
				"value", raw, "default", defaultReflectInterval, "error", err)
		}
	}

	dispatcher := reflectplugin.NewDispatcher(deps)
	if err := svc.RegisterBuiltin(scheduler.BuiltinJob{
		Name:      reflectBuiltinJobName,
		Schedule:  scheduler.Schedule{Every: every.String()},
		ExecScope: scheduler.ExecScopeSystem,
		Handler:   dispatcher.Handle,
	}); err != nil {
		return fmt.Errorf("register reflect builtin: %w", err)
	}
	return nil
}
