package runner

import (
	"context"
	"fmt"
	"runtime"

	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

// sandboxBackend abstracts runner sandbox backend lifecycle so boxsh can be
// replaced by other implementations in the future.
type sandboxBackend interface {
	Runtime() pkgplugins.SandboxRuntime
	Boxsh() *boxshclient.SharedBackend
	SessionDir() string
	Alive() bool
	Close() error
}

type sandboxBackendFactory func(context.Context, GoRunnerConfig) (sandboxBackend, error)

type noopSandboxBackend struct{}

func (noopSandboxBackend) Runtime() pkgplugins.SandboxRuntime {
	return plugintools.SandboxRuntimeFromBackend(nil)
}
func (noopSandboxBackend) Boxsh() *boxshclient.SharedBackend { return nil }
func (noopSandboxBackend) SessionDir() string                { return "" }
func (noopSandboxBackend) Alive() bool                       { return true }
func (noopSandboxBackend) Close() error                      { return nil }

type boxshSandboxBackend struct {
	backend *boxshclient.SharedBackend
}

func (b boxshSandboxBackend) Runtime() pkgplugins.SandboxRuntime {
	return plugintools.SandboxRuntimeFromBackend(b.backend)
}
func (b boxshSandboxBackend) Boxsh() *boxshclient.SharedBackend { return b.backend }
func (b boxshSandboxBackend) SessionDir() string {
	if b.backend == nil {
		return ""
	}
	return b.backend.SessionDir()
}

func (b boxshSandboxBackend) Alive() bool {
	return b.backend != nil && b.backend.Alive()
}

func (b boxshSandboxBackend) Close() error {
	if b.backend == nil {
		return nil
	}
	return b.backend.Close()
}

func resolveSandboxBackend(ctx context.Context, cfg GoRunnerConfig) (sandboxBackend, error) {
	name := cfg.Sandbox.BackendName()
	if cfg.DisableSandbox {
		name = "noop"
	}

	if name == "auto" {
		if boxshclient.PlatformSupportsBoxsh() {
			name = "boxsh"
		} else {
			name = "noop"
		}
	}

	factory, ok := sandboxBackendFactories[name]
	if !ok {
		return nil, fmt.Errorf("unknown sandbox backend: %q", name)
	}
	return factory(ctx, cfg)
}

var sandboxBackendFactories = map[string]sandboxBackendFactory{
	"noop": func(context.Context, GoRunnerConfig) (sandboxBackend, error) {
		return noopSandboxBackend{}, nil
	},
	"boxsh": func(ctx context.Context, cfg GoRunnerConfig) (sandboxBackend, error) {
		if !boxshclient.PlatformSupportsBoxsh() {
			return nil, fmt.Errorf("sandbox backend %q is not supported on %s", "boxsh", runtime.GOOS)
		}
		backend, err := createAndStartBackend(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return boxshSandboxBackend{backend: backend}, nil
	},
}
