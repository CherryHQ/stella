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
	// readinessDrainDelayEnv delays the start of HTTP shutdown after readiness
	// flips to not-ready, giving a load balancer time to observe the failing
	// /readyz probe and stop routing new traffic before connections are cut.
	readinessDrainDelayEnv = "STELLA_READINESS_DRAIN_DELAY"
)

const (
	defaultHTTPShutdownTimeout  = 60 * time.Second
	defaultRiverSoftStopTimeout = 120 * time.Second
	defaultReadinessDrainDelay  = 0 * time.Second
)

// Lifecycle holds the parsed shutdown-lifecycle durations. Parse it once at
// startup (LoadLifecycle) and inject the values; the hot path must not re-read
// the environment.
type Lifecycle struct {
	// HTTPShutdownTimeout is the drain budget for in-flight HTTP requests.
	HTTPShutdownTimeout time.Duration
	// RiverSoftStopTimeout is the drain budget for in-flight background jobs.
	RiverSoftStopTimeout time.Duration
	// ReadinessDrainDelay is the wait between flipping not-ready and starting
	// HTTP shutdown, so load balancers stop routing before connections close.
	ReadinessDrainDelay time.Duration
}

// LoadLifecycle parses the shutdown-lifecycle env vars, applying defaults and
// failing fast on an unparseable value or a bound violation. Call it once early
// in startup so a misconfiguration surfaces before any subsystem starts.
func LoadLifecycle() (Lifecycle, error) {
	httpTimeout, err := HTTPShutdownTimeout()
	if err != nil {
		return Lifecycle{}, err
	}
	riverTimeout, err := RiverSoftStopTimeout()
	if err != nil {
		return Lifecycle{}, err
	}
	drainDelay, err := ReadinessDrainDelay()
	if err != nil {
		return Lifecycle{}, err
	}
	return Lifecycle{
		HTTPShutdownTimeout:  httpTimeout,
		RiverSoftStopTimeout: riverTimeout,
		ReadinessDrainDelay:  drainDelay,
	}, nil
}

// HTTPShutdownTimeout returns the HTTP drain budget. Must be greater than zero.
func HTTPShutdownTimeout() (time.Duration, error) {
	return parseDurationEnv(httpShutdownTimeoutEnv, defaultHTTPShutdownTimeout, false)
}

// RiverSoftStopTimeout returns the background-job drain budget. Must be greater
// than zero.
func RiverSoftStopTimeout() (time.Duration, error) {
	return parseDurationEnv(riverSoftStopTimeoutEnv, defaultRiverSoftStopTimeout, false)
}

// ReadinessDrainDelay returns the pre-shutdown readiness delay. Zero is allowed
// (no delay); negative is rejected.
func ReadinessDrainDelay() (time.Duration, error) {
	return parseDurationEnv(readinessDrainDelayEnv, defaultReadinessDrainDelay, true)
}

// parseDurationEnv reads a Go duration (time.ParseDuration) from name, returning
// def when unset or empty. It rejects unparseable values, negatives, and — when
// allowZero is false — zero, with an actionable error instead of silently
// falling back so a typo fails fast.
func parseDurationEnv(name string, def time.Duration, allowZero bool) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a valid duration: use a Go duration such as 60s, 2m, or 500ms", name, v)
	}
	if d < 0 || (!allowZero && d == 0) {
		bound := "greater than zero"
		if allowZero {
			bound = "zero or greater"
		}
		return 0, fmt.Errorf("%s=%q must be %s", name, v, bound)
	}
	return d, nil
}
