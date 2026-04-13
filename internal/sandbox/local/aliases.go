package local

import sandboxpkg "github.com/vaayne/anna/internal/sandbox"

const (
	NetworkDisabled  = sandboxpkg.NetworkDisabled
	NetworkAllowAll  = sandboxpkg.NetworkAllowAll
	NetworkWhitelist = sandboxpkg.NetworkWhitelist
)

type (
	Policy                   = sandboxpkg.Policy
	Session                  = sandboxpkg.Session
	Host                     = sandboxpkg.Host
	Factory                  = sandboxpkg.Factory
	ReadResult               = sandboxpkg.ReadResult
	WriteResult              = sandboxpkg.WriteResult
	Edit                     = sandboxpkg.Edit
	EditResult               = sandboxpkg.EditResult
	StatResult               = sandboxpkg.StatResult
	DirEntry                 = sandboxpkg.DirEntry
	TempFile                 = sandboxpkg.TempFile
	ExecOptions              = sandboxpkg.ExecOptions
	ExecResult               = sandboxpkg.ExecResult
	ProcessRequest           = sandboxpkg.ProcessRequest
	ProcessHandle            = sandboxpkg.ProcessHandle
	HTTPOptions              = sandboxpkg.HTTPOptions
	HTTPResult               = sandboxpkg.HTTPResult
	HTTPStream               = sandboxpkg.HTTPStream
	PolicyCompatibilityError = sandboxpkg.PolicyCompatibilityError
)

func nextSessionID() string { return sandboxpkg.NewSessionID() }

func logSessionCreated(sessionID, backend string, policy Policy) {
	sandboxpkg.LogSessionCreated(sessionID, backend, policy)
}

func logSessionClosed(sessionID, backend, reason string) {
	sandboxpkg.LogSessionClosed(sessionID, backend, reason)
}

func logRelaxedMode(sessionID, backend, reason string, policy Policy, warnings ...string) {
	sandboxpkg.LogRelaxedMode(sessionID, backend, reason, policy, warnings...)
}

func logPolicyDenied(sessionID, backend, operation, resource, reason string) {
	sandboxpkg.LogPolicyDenied(sessionID, backend, operation, resource, reason)
}

func NewFactory() sandboxpkg.Factory { return &localFactory{} }
