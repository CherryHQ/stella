package lcm

import (
	"context"
	"errors"

	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func init() {
	pkgplugins.Register("memory/lcm", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.Registry().RegisterMetadata(pkgplugins.PluginMeta{
			ID:           "memory/lcm",
			Kind:         "memory",
			Name:         "lcm",
			DisplayName:  "Lossless Context Management",
			Description:  "Hierarchical summary-based memory with full-history preservation.",
			AdminVisible: true,
			Capabilities: []string{
				pkgplugins.CapabilityMemory,
			},
		})
		host.Registry().RegisterMemory(pkgplugins.MemoryRegistration{
			PluginID: "memory/lcm",
			Name:     "lcm",
			Build: func(ctx context.Context, build pkgplugins.MemoryContext) (memory.Provider, error) {
				if build.DB == nil {
					return nil, errors.New("lcm plugin requires a shared DB connection")
				}
				return New(build.DB, build.SummarizerFn, build.State.Config)
			},
		})
	}))
}
