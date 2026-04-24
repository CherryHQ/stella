package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

// NewSessionID returns a cryptographically random session identifier.
// Using random IDs avoids Docker container name collisions across test runs.
func NewSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sandbox: NewSessionID: " + err.Error())
	}
	return "sandbox-" + hex.EncodeToString(b[:])
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
