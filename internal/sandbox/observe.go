package sandbox

import (
	"log/slog"
	"sync/atomic"
)

var sandboxSessionSeq uint64

func nextSessionID() string {
	id := atomic.AddUint64(&sandboxSessionSeq, 1)
	return "sandbox-" + itoa(id)
}

// NewSessionID returns a unique session identifier for backend implementations.
func NewSessionID() string {
	return nextSessionID()
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func logSessionCreated(sessionID, backend string, policy Policy) {
	slog.Info("sandbox.session_created",
		"session_id", sessionID,
		"backend", backend,
		"relaxed", policy.Relaxed,
		"network_mode", policy.NetworkModeOrDefault(),
		"working_dir", policy.Filesystem.WorkingDir,
	)
}

// LogSessionCreated records backend session creation.
func LogSessionCreated(sessionID, backend string, policy Policy) {
	logSessionCreated(sessionID, backend, policy)
}

func logSessionClosed(sessionID, backend, reason string) {
	slog.Info("sandbox.session_closed",
		"session_id", sessionID,
		"backend", backend,
		"reason", reason,
	)
}

// LogSessionClosed records backend session closure.
func LogSessionClosed(sessionID, backend, reason string) {
	logSessionClosed(sessionID, backend, reason)
}

func logRelaxedMode(sessionID, backend, reason string, policy Policy, warnings ...string) {
	slog.Warn("sandbox.relaxed_mode",
		"session_id", sessionID,
		"backend", backend,
		"reason", reason,
		"warnings", warnings,
		"network_mode", policy.NetworkModeOrDefault(),
		"working_dir", policy.Filesystem.WorkingDir,
	)
}

// LogRelaxedMode records when a backend runs in explicit relaxed mode.
func LogRelaxedMode(sessionID, backend, reason string, policy Policy, warnings ...string) {
	logRelaxedMode(sessionID, backend, reason, policy, warnings...)
}

func logUnsupportedBackend(policy Policy, attempted []string, reason string) {
	slog.Error("sandbox.unsupported_backend",
		"backend", policy.Backend,
		"relaxed", policy.Relaxed,
		"attempted_backends", attempted,
		"reason", reason,
		"network_mode", policy.NetworkModeOrDefault(),
		"working_dir", policy.Filesystem.WorkingDir,
	)
}

func logPolicyDenied(sessionID, backend, operation, resource, reason string) {
	slog.Error("sandbox.policy_denied",
		"session_id", sessionID,
		"backend", backend,
		"operation", operation,
		"resource", resource,
		"reason", reason,
	)
}

// LogPolicyDenied records a backend policy denial.
func LogPolicyDenied(sessionID, backend, operation, resource, reason string) {
	logPolicyDenied(sessionID, backend, operation, resource, reason)
}

// LogExceptionPath records an explicit execution-path exception outside host mediation.
func LogExceptionPath(exceptionID, component, accessType, detail string) {
	slog.Warn("sandbox.exception_path",
		"exception_id", exceptionID,
		"component", component,
		"access_type", accessType,
		"detail", detail,
	)
}
