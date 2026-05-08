package sandbox

import sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"

type (
	Session        = sandboxpkg.Session
	Host           = sandboxpkg.Host
	DirEntry       = sandboxpkg.DirEntry
	ExecOptions    = sandboxpkg.ExecOptions
	ExecResult     = sandboxpkg.ExecResult
	ProcessRequest = sandboxpkg.ProcessRequest
	ProcessHandle  = sandboxpkg.ProcessHandle
)

func NopSession() Session {
	return sandboxpkg.NopSession()
}
