package lcm

import (
	"context"
	"errors"

	"github.com/CherryHQ/stella/internal/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func init() {
	pkgplugins.Register("memory/lcm", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
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
		host.AddMemory(pkgplugins.MemorySpec{
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
