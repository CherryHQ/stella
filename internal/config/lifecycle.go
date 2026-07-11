package config

import "time"

// Lifecycle env vars tune graceful shutdown for managed (Kubernetes)
// deployment. They bound how long the two-phase drain waits at each stage; see
// the deployment docs for how they combine into terminationGracePeriodSeconds.
// The values are parsed once at startup as part of ServerConfig
// (STELLA_HTTP_SHUTDOWN_TIMEOUT / STELLA_RIVER_SOFT_STOP_TIMEOUT) and injected;
// the hot path must not re-read the environment.

const (
	defaultHTTPShutdownTimeout  = 60 * time.Second
	defaultRiverSoftStopTimeout = 120 * time.Second
)

// Lifecycle holds the parsed shutdown-lifecycle durations. It is a nested group
// of ServerConfig; #700's injection (setup/runServer taking a config.Lifecycle)
// depends on this type staying stable.
type Lifecycle struct {
	// HTTPShutdownTimeout is the drain budget for in-flight HTTP requests. When
	// the budget is spent the server force-closes any still-open connections.
	HTTPShutdownTimeout time.Duration
	// RiverSoftStopTimeout is the drain budget for in-flight background jobs
	// (goal/scheduler agent runs) before River cancels their work contexts and
	// escalates to a hard stop.
	RiverSoftStopTimeout time.Duration
}
