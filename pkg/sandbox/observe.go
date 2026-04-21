package sandbox

import (
	"log/slog"
	"sync/atomic"
)

var sessionSeq uint64

// NewSessionID returns a unique session identifier for backend implementations.
func NewSessionID() string {
	id := atomic.AddUint64(&sessionSeq, 1)
	return "sandbox-" + itoa(id)
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

func LogSessionCreated(sessionID, backend string, policy Policy) {
	slog.Info("sandbox.session_created",
		"session_id", sessionID,
		"backend", backend,
		"network_mode", policy.NetworkModeOrDefault(),
		"working_dir", policy.Filesystem.WorkingDir,
	)
}

func LogSessionClosed(sessionID, backend, reason string) {
	slog.Info("sandbox.session_closed",
		"session_id", sessionID,
		"backend", backend,
		"reason", reason,
	)
}

func LogUnsupportedBackend(policy Policy, attempted []string, reason string) {
	slog.Error("sandbox.unsupported_backend",
		"backend", policy.Backend,
		"attempted_backends", attempted,
		"reason", reason,
		"network_mode", policy.NetworkModeOrDefault(),
		"working_dir", policy.Filesystem.WorkingDir,
	)
}

func LogPolicyDenied(sessionID, backend, operation, resource, reason string) {
	slog.Error("sandbox.policy_denied",
		"session_id", sessionID,
		"backend", backend,
		"operation", operation,
		"resource", resource,
		"reason", reason,
	)
}

func LogExceptionPath(exceptionID, component, accessType, detail string) {
	slog.Warn("sandbox.exception_path",
		"exception_id", exceptionID,
		"component", component,
		"access_type", accessType,
		"detail", detail,
	)
}
