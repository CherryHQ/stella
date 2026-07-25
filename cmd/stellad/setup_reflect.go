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
	defaultReflectCron     = "0 0,6,12,18 * * *"
	// minReflectInterval guards against runaway-cost misconfiguration. The
	// override is a dev knob; setting it below this is almost certainly a
	// mistake (e.g. STELLA_REFLECT_INTERVAL=1ns) and would fire reflect per
	// org continuously, hammering the configured LLM providers.
	minReflectInterval = time.Minute
)

// registerReflectBuiltin wires the reflect review cycle into the scheduler
// as a builtin job. The cadence defaults to 6h and can be overridden
// for development via STELLA_REFLECT_INTERVAL (Go duration string), threaded in
// as intervalRaw; reflectModeRaw and curatorModeRaw carry the independent
// rollout-compatibility and lifecycle controls. Reflect is structured-only;
// reflectModeRaw exists for one transition release so stale legacy deployment
// configuration fails clearly. The interval override is intentionally omitted
// from user-facing docs: it exists so development wiring can be verified without
// a rebuild. Parsing stays here (not in the config layer) so the interval keeps
// its lenient warn-and-clamp behavior while mode validation fails fast before
// the scheduler job is registered.
func registerReflectBuiltin(svc *scheduler.Service, cfg reflect.Config, intervalRaw, reflectModeRaw, curatorModeRaw string) error {
	if err := validateReflectModeCompatibility(reflectModeRaw); err != nil {
		return err
	}
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
		Schedule: resolveReflectSchedule(intervalRaw),
		Handler:  handler,
	}); err != nil {
		return fmt.Errorf("register reflect builtin: %w", err)
	}
	slog.Info("reflect: registered scheduler builtin",
		"schedule", resolveReflectSchedule(intervalRaw),
		"usage_curator_mode", usageCuratorSettings.Mode,
	)
	return nil
}

func resolveReflectSchedule(rawInterval string) scheduler.Schedule {
	if rawInterval == "" {
		return scheduler.Schedule{Cron: defaultReflectCron}
	}
	return scheduler.Schedule{Every: resolveReflectInterval(rawInterval).String()}
}

func validateReflectModeCompatibility(rawMode string) error {
	raw := strings.TrimSpace(rawMode)
	switch strings.ToLower(raw) {
	case "", "structured":
		return nil
	case "legacy":
		return fmt.Errorf("reflect: STELLA_REFLECT_MODE=legacy is no longer supported; remove the setting to use structured Reflect")
	default:
		return fmt.Errorf("reflect: unsupported STELLA_REFLECT_MODE %q; structured Reflect is always enabled", raw)
	}
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
