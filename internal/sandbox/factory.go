package sandbox

import (
	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
	dockerplugin "github.com/vaayne/anna/plugins/sandbox/docker"
	noneplugin "github.com/vaayne/anna/plugins/sandbox/none"
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
	mustRegisterFactory(r, dockerplugin.NewFactory(dockerplugin.Config{}), true)
	mustRegisterFactory(r, noneplugin.NewFactory(), true)
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
