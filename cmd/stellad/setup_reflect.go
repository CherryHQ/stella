package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/CherryHQ/stella/internal/reflect"
	"github.com/CherryHQ/stella/internal/scheduler"
)

const (
	defaultReflectInterval = 6 * time.Hour
	// minReflectInterval guards against runaway-cost misconfiguration. The
	// override is a dev knob; setting it below this is almost certainly a
	// mistake (e.g. STELLA_REFLECT_INTERVAL=1ns) and would fire reflect per
	// org continuously, hammering the configured LLM providers.
	minReflectInterval = time.Minute
)

// registerReflectBuiltin wires the reflect review cycle into the scheduler
// as a builtin job. The cadence defaults to 6h and can be overridden
// for development via the STELLA_REFLECT_INTERVAL env var (Go duration
// string). The override is intentionally undocumented in user-facing docs
// — it exists so verifying reflect's wiring doesn't require a code rebuild.
func registerReflectBuiltin(svc *scheduler.Service, cfg reflect.Config) error {
	every := resolveReflectInterval()

	handler, err := reflect.NewBuiltinHandler(cfg)
	if err != nil {
		return fmt.Errorf("build reflect handler: %w", err)
	}
	if err := svc.RegisterBuiltin(scheduler.BuiltinJob{
		Name:     reflect.BuiltinJobName,
		Schedule: scheduler.Schedule{Every: every.String()},
		Handler:  handler,
	}); err != nil {
		return fmt.Errorf("register reflect builtin: %w", err)
	}
	slog.Info("reflect: registered scheduler builtin", "every", every)
	return nil
}

func resolveReflectInterval() time.Duration {
	raw := os.Getenv("STELLA_REFLECT_INTERVAL")
	if raw == "" {
		return defaultReflectInterval
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("reflect: STELLA_REFLECT_INTERVAL unparseable, using default",
			"value", raw, "default", defaultReflectInterval, "error", err)
		return defaultReflectInterval
	}
	if parsed < minReflectInterval {
		slog.Warn("reflect: STELLA_REFLECT_INTERVAL below minimum, using minimum",
			"value", parsed, "min", minReflectInterval)
		return minReflectInterval
	}
	return parsed
}
