package plugintools

import (
	"context"
	"fmt"
	"time"

	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type boxshSandboxRuntime struct {
	backend *boxshclient.SharedBackend
}

type hostSandboxRuntime struct {
	host sandbox.Host
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

// SandboxRuntimeFromHost adapts a sandbox host to the plugin-facing sandbox runtime interface.
func SandboxRuntimeFromHost(host sandbox.Host) pkgplugins.SandboxRuntime {
	if host == nil {
		return disabledSandboxRuntime{}
	}
	return hostSandboxRuntime{host: host}
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

func (r hostSandboxRuntime) Enabled() bool {
	return r.host != nil
}

func (r hostSandboxRuntime) Exec(ctx context.Context, command string, timeoutSeconds int) (pkgplugins.SandboxExecResult, error) {
	if r.host == nil {
		return pkgplugins.SandboxExecResult{}, fmt.Errorf("sandbox runtime: host is not configured")
	}
	result, err := r.host.Exec(ctx, command, sandbox.ExecOptions{Timeout: time.Duration(timeoutSeconds) * time.Second})
	if err != nil {
		return pkgplugins.SandboxExecResult{}, err
	}
	return pkgplugins.SandboxExecResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	}, nil
}
