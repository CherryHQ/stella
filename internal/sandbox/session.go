package sandbox

import sandboxpkg "github.com/vaayne/anna/pkg/sandbox"

type (
	Session        = sandboxpkg.Session
	Host           = sandboxpkg.Host
	ReadResult     = sandboxpkg.ReadResult
	WriteResult    = sandboxpkg.WriteResult
	Edit           = sandboxpkg.Edit
	EditResult     = sandboxpkg.EditResult
	StatResult     = sandboxpkg.StatResult
	DirEntry       = sandboxpkg.DirEntry
	TempFile       = sandboxpkg.TempFile
	ExecOptions    = sandboxpkg.ExecOptions
	ExecResult     = sandboxpkg.ExecResult
	ProcessRequest = sandboxpkg.ProcessRequest
	ProcessHandle  = sandboxpkg.ProcessHandle
	HTTPOptions    = sandboxpkg.HTTPOptions
	HTTPResult     = sandboxpkg.HTTPResult
	HTTPStream     = sandboxpkg.HTTPStream
)

func NopSession() Session {
	return sandboxpkg.NopSession()
}
