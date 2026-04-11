package runner

import (
	"context"

	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

// sandboxBackend abstracts runner sandbox backend lifecycle so boxsh can be
// replaced by other implementations in the future.
type sandboxBackend interface {
	Runtime() pkgplugins.SandboxRuntime
	Boxsh() *boxshclient.SharedBackend
	Alive() bool
	Close() error
}

type noopSandboxBackend struct{}

func (noopSandboxBackend) Runtime() pkgplugins.SandboxRuntime {
	return plugintools.SandboxRuntimeFromBackend(nil)
}
func (noopSandboxBackend) Boxsh() *boxshclient.SharedBackend { return nil }
func (noopSandboxBackend) Alive() bool                       { return true }
func (noopSandboxBackend) Close() error                      { return nil }

type boxshSandboxBackend struct {
	backend *boxshclient.SharedBackend
}

func (b boxshSandboxBackend) Runtime() pkgplugins.SandboxRuntime {
	return plugintools.SandboxRuntimeFromBackend(b.backend)
}
func (b boxshSandboxBackend) Boxsh() *boxshclient.SharedBackend { return b.backend }
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
	if cfg.DisableSandbox || !boxshclient.PlatformSupportsBoxsh() {
		return noopSandboxBackend{}, nil
	}
	backend, err := createAndStartBackend(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return boxshSandboxBackend{backend: backend}, nil
}

