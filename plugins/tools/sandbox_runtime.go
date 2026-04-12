package plugintools

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type boxshSandboxRuntime struct {
	backend *boxshclient.SharedBackend
}

type disabledSandboxRuntime struct{}

// SandboxRuntimeFromBackend adapts a boxsh shared backend to the plugin-facing
// sandbox runtime interface.
func SandboxRuntimeFromBackend(backend *boxshclient.SharedBackend) pkgplugins.SandboxRuntime {
	if backend == nil {
		return disabledSandboxRuntime{}
	}
	return boxshSandboxRuntime{backend: backend}
}

func (disabledSandboxRuntime) Enabled() bool { return false }

func (disabledSandboxRuntime) Exec(context.Context, string, int) (pkgplugins.SandboxExecResult, error) {
	return pkgplugins.SandboxExecResult{}, fmt.Errorf("sandbox runtime: unavailable on this platform/session")
}

func (r boxshSandboxRuntime) Enabled() bool {
	return r.backend != nil && r.backend.Alive()
}

func (r boxshSandboxRuntime) Exec(context.Context, string, int) (pkgplugins.SandboxExecResult, error) {
	if r.backend == nil {
		return pkgplugins.SandboxExecResult{}, fmt.Errorf("sandbox runtime: backend is not configured")
	}
	if !r.backend.Alive() {
		return pkgplugins.SandboxExecResult{}, fmt.Errorf("sandbox runtime: backend is not running")
	}
	return pkgplugins.SandboxExecResult{}, fmt.Errorf("sandbox runtime: direct plugin Exec is fail-closed in Phase 2; use the sandbox session/host path before enabling execution")
}
