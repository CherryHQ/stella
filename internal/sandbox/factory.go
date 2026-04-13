package sandbox

import (
	"runtime"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
	localplugin "github.com/vaayne/anna/plugins/sandbox/local"
)

type (
	Factory  = sandboxpkg.Factory
	Registry = sandboxpkg.Registry
)

var defaultRegistry = DefaultRegistry()

func GlobalRegistry() *Registry {
	return defaultRegistry
}

func NewRegistry() *Registry {
	return sandboxpkg.NewRegistry()
}

func DefaultRegistry() *Registry {
	r := NewRegistry()
	mustRegisterFactory(r, &boxshFactory{}, PlatformSupportsBoxsh())
	mustRegisterFactory(r, localplugin.NewFactory(), true)
	return r
}

func mustRegisterFactory(r *Registry, factory Factory, enabled bool) {
	if !enabled {
		return
	}
	if err := r.Register(factory); err != nil {
		panic(err)
	}
}

func PlatformSupportsBoxsh() bool {
	return PlatformRequiresBoxsh()
}

func PlatformRequiresBoxsh() bool {
	switch runtime.GOOS {
	case "linux", "darwin":
		return true
	default:
		return false
	}
}
