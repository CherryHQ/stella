package defaults

import (
	sandboxpkg "github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/internal/sandbox/boxsh"
	"github.com/vaayne/anna/internal/sandbox/local"
)

var defaultRegistry = DefaultRegistry()

func GlobalRegistry() *sandboxpkg.Registry {
	return defaultRegistry
}

func DefaultRegistry() *sandboxpkg.Registry {
	r := sandboxpkg.NewRegistry()
	mustRegister(r, boxsh.NewFactory(), sandboxpkg.PlatformSupportsBoxsh())
	mustRegister(r, local.NewFactory(), true)
	return r
}

func mustRegister(r *sandboxpkg.Registry, factory sandboxpkg.Factory, enabled bool) {
	if !enabled {
		return
	}
	if err := r.Register(factory); err != nil {
		panic(err)
	}
}
