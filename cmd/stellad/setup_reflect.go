package main

import (
	"fmt"
	"log/slog"
	"strings"
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
// for development via STELLA_REFLECT_INTERVAL (Go duration string), threaded in
// as intervalRaw; curatorModeRaw controls lifecycle writes independently.
// Reflect is structured-only. The interval override is intentionally omitted
// from user-facing docs: it exists so development wiring can be verified without
// a rebuild. Parsing stays here (not in the config layer) so the interval keeps
// its lenient warn-and-clamp behavior while mode validation fails fast before
// the scheduler job is registered.
func registerReflectBuiltin(svc *scheduler.Service, cfg reflect.Config, intervalRaw, curatorModeRaw string) error {
	every := resolveReflectInterval(intervalRaw)
	usageCuratorSettings, err := resolveUsageCuratorSettings(curatorModeRaw)
	if err != nil {
		return err
	}
	cfg.UsageCuratorSettings = usageCuratorSettings

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
	slog.Info("reflect: registered scheduler builtin", "every", every, "usage_curator_mode", usageCuratorSettings.Mode)
	return nil
}

func resolveReflectInterval(raw string) time.Duration {
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

func resolveUsageCuratorSettings(rawMode string) (reflect.UsageCuratorSettings, error) {
	raw := strings.TrimSpace(rawMode)
	if raw == "" {
		return reflect.UsageCuratorSettings{Mode: reflect.UsageCuratorModeArmed}, nil
	}
	switch strings.ToLower(raw) {
	case string(reflect.UsageCuratorModeShadow):
		return reflect.UsageCuratorSettings{Mode: reflect.UsageCuratorModeShadow}, nil
	case string(reflect.UsageCuratorModeArmed):
		return reflect.UsageCuratorSettings{Mode: reflect.UsageCuratorModeArmed}, nil
	default:
		return reflect.UsageCuratorSettings{}, fmt.Errorf("reflect: unsupported STELLA_REFLECT_CURATOR_MODE %q (want shadow or armed)", raw)
	}
}
