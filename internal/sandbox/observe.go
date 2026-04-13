package sandbox

import sandboxpkg "github.com/vaayne/anna/pkg/sandbox"

func NewSessionID() string {
	return sandboxpkg.NewSessionID()
}

func LogSessionCreated(sessionID, backend string, policy Policy) {
	sandboxpkg.LogSessionCreated(sessionID, backend, policy)
}

func LogSessionClosed(sessionID, backend, reason string) {
	sandboxpkg.LogSessionClosed(sessionID, backend, reason)
}

func LogRelaxedMode(sessionID, backend, reason string, policy Policy, warnings ...string) {
	sandboxpkg.LogRelaxedMode(sessionID, backend, reason, policy, warnings...)
}

func LogPolicyDenied(sessionID, backend, operation, resource, reason string) {
	sandboxpkg.LogPolicyDenied(sessionID, backend, operation, resource, reason)
}

func LogExceptionPath(exceptionID, component, accessType, detail string) {
	sandboxpkg.LogExceptionPath(exceptionID, component, accessType, detail)
}
