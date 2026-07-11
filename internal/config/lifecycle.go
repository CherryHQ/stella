package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Lifecycle env vars tune graceful shutdown for managed (Kubernetes) deployment.
// They bound how long the two-phase drain waits at each stage; see the deployment
// docs for how they combine into terminationGracePeriodSeconds.
const (
	// httpShutdownTimeoutEnv bounds how long the server waits for in-flight HTTP
	// requests to finish after it stops accepting new ones. When the budget is
	// spent the server force-closes any still-open connections.
	httpShutdownTimeoutEnv = "STELLA_HTTP_SHUTDOWN_TIMEOUT"
	// riverSoftStopTimeoutEnv bounds how long River waits for in-flight background
	// jobs (goal/scheduler agent runs) to finish during shutdown before it cancels
	// their work contexts, escalating to a hard stop.
	riverSoftStopTimeoutEnv = "STELLA_RIVER_SOFT_STOP_TIMEOUT"
)

const (
	defaultHTTPShutdownTimeout  = 60 * time.Second
	defaultRiverSoftStopTimeout = 120 * time.Second
)

// Lifecycle holds the parsed shutdown-lifecycle durations. Parse it once at
// startup (LoadLifecycle) and inject the values; the hot path must not re-read
// the environment.
type Lifecycle struct {
	// HTTPShutdownTimeout is the drain budget for in-flight HTTP requests.
	HTTPShutdownTimeout time.Duration
	// RiverSoftStopTimeout is the drain budget for in-flight background jobs.
	RiverSoftStopTimeout time.Duration
}

// LoadLifecycle parses the shutdown-lifecycle env vars, applying defaults and
// failing fast on an unparseable value or a bound violation. Call it once early
// in startup so a misconfiguration surfaces before any subsystem starts.
func LoadLifecycle() (Lifecycle, error) {
	httpTimeout, err := parseDurationEnv(httpShutdownTimeoutEnv, defaultHTTPShutdownTimeout)
	if err != nil {
		return Lifecycle{}, err
	}
	riverTimeout, err := parseDurationEnv(riverSoftStopTimeoutEnv, defaultRiverSoftStopTimeout)
	if err != nil {
		return Lifecycle{}, err
	}
	return Lifecycle{
		HTTPShutdownTimeout:  httpTimeout,
		RiverSoftStopTimeout: riverTimeout,
	}, nil
}

// parseDurationEnv reads a Go duration (time.ParseDuration) from name, returning
// def when unset or empty. It rejects unparseable and non-positive values with
// an actionable error instead of silently falling back, so a typo fails fast.
func parseDurationEnv(name string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a valid duration: use a Go duration such as 60s, 2m, or 500ms", name, v)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s=%q must be greater than zero", name, v)
	}
	return d, nil
}
