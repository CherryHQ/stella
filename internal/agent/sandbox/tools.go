package sandbox

import (
	"fmt"
	"path"
	"strings"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// NewTools returns the four standard sandbox-backed tools. bash retains the
// Session process capability; read, write, and edit use only Filesystem.
func NewTools(session pkgsandbox.Session, toolsBinDir string, sessionSecretValues *SessionSecretValues) []pkgtools.Tool {
	if session == nil {
		return nil
	}
	fsSession, ok := session.(pkgsandbox.FilesystemSession)
	if !ok {
		return nil
	}
	// Fail runner construction closed when the provider cannot open the mediated
	// filesystem the read/write/edit tools require. A nil Filesystem with a nil
	// error is treated as a failure, never dereferenced. Each tool opens its own
	// short-lived Filesystem per call below.
	probe, err := fsSession.Filesystem()
	if err != nil || probe == nil {
		return nil
	}
	_ = probe.Close()
	return []pkgtools.Tool{
		newBashTool(session, toolsBinDir, session.WorkingDir(), sessionSecretValues),
		newReadTool(fsSession),
		newWriteTool(fsSession),
		newEditTool(fsSession),
	}
}

// ToolDefinitions returns the canonical definitions for all core tools.
// No sandbox session is required — useful for API metadata endpoints.
func ToolDefinitions() []pkgtools.Definition {
	return []pkgtools.Definition{bashDefinition(), readDefinition(), writeDefinition(), editDefinition()}
}

// resolveToolPath returns a canonical sandbox path. Relative paths deliberately
// use the active Session's logical working directory, never the caller's host
// project root.
func resolveToolPath(session pkgsandbox.Session, input string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("sandbox session is required")
	}
	resolved := input
	if strings.HasPrefix(input, "$") {
		policy := session.Policy()
		var err error
		resolved, err = pkgsandbox.ExpandPathVariables(input, policy.Env)
		if err != nil {
			return "", err
		}
		// Variables are trusted session-policy aliases. Providers alone know how
		// to project their physical process view into canonical Filesystem paths.
		projector, ok := session.(pkgsandbox.FilesystemPathProjector)
		if !ok {
			return "", fmt.Errorf("sandbox session cannot project filesystem paths")
		}
		var projected bool
		resolved, projected = projector.ProjectFilesystemPath(resolved)
		if !projected {
			return "", fmt.Errorf("sandbox filesystem path %q is not mapped", input)
		}
	}
	if !strings.HasPrefix(resolved, "/") {
		resolved = path.Join(session.WorkingDir(), resolved)
	}
	return path.Clean(resolved), nil
}

// withFilesystem opens a short-lived mediated Filesystem for one tool operation
// and always closes it. read/write/edit each canonicalize their path first, so
// the Filesystem only ever sees confined canonical coordinates.
func withFilesystem(session pkgsandbox.FilesystemSession, fn func(pkgsandbox.Filesystem) error) error {
	fs, err := session.Filesystem()
	if err != nil {
		return err
	}
	// A provider must never hand back a nil Filesystem with a nil error: reject it
	// rather than dereference it, and never fall back to host I/O.
	if fs == nil {
		return fmt.Errorf("sandbox returned a nil filesystem")
	}
	defer func() { _ = fs.Close() }()
	return fn(fs)
}

func toolIntArg(args map[string]any, key string, defaultVal int) int {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	default:
		return defaultVal
	}
}
