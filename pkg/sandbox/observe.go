package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

type (
	sessionIDKey        struct{}
	processRegistrarKey struct{}
	processIdentityKey  struct{}
)

type ProcessIdentity struct {
	PID       int
	StartTime uint64
}

type ProcessRegistrar func(context.Context, ProcessIdentity) error

func WithProcessRegistrar(ctx context.Context, registrar ProcessRegistrar) context.Context {
	return context.WithValue(ctx, processRegistrarKey{}, registrar)
}

func ProcessRegistrarFromContext(ctx context.Context) ProcessRegistrar {
	registrar, _ := ctx.Value(processRegistrarKey{}).(ProcessRegistrar)
	return registrar
}

func WithProcessIdentities(ctx context.Context, identities []ProcessIdentity) context.Context {
	return context.WithValue(ctx, processIdentityKey{}, identities)
}

func ProcessIdentities(ctx context.Context) []ProcessIdentity {
	identities, _ := ctx.Value(processIdentityKey{}).([]ProcessIdentity)
	return identities
}

// EnvResourceID labels host processes for diagnostics. Cleanup authority is the
// gated PID/start-time identity persisted before target execution, never an
// environment scan that unrelated protected processes could make ambiguous.
const EnvResourceID = "STELLA_SANDBOX_RESOURCE_ID"

// NewSessionID returns a cryptographically random session identifier.
// Using random IDs avoids Docker container name collisions across test runs.
func NewSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sandbox: NewSessionID: " + err.Error())
	}
	return "sandbox-" + hex.EncodeToString(b[:])
}

// WithSessionID pins a pre-registered durable compute identity into backend
// creation. Providers use the same value for reconstructable resources (for
// example a Docker container name), closing the crash window between an
// external create and publishing that identity to PostgreSQL.
func WithSessionID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDKey{}, id)
}

// SessionID returns the pre-registered identity when one is present and mints
// a process-local identity otherwise.
func SessionID(ctx context.Context) string {
	if id, _ := ctx.Value(sessionIDKey{}).(string); id != "" {
		return id
	}
	return NewSessionID()
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
