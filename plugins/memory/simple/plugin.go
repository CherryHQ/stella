package simple

import (
	"context"
	"errors"

	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func init() {
	pkgplugins.Register("memory/simple", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.Registry().RegisterMetadata(pkgplugins.PluginMeta{
			ID:           "memory/simple",
			Kind:         "memory",
			Name:         "simple",
			DisplayName:  "Simple Sliding Window",
			Description:  "Sliding-window memory without compaction or summaries.",
			AdminVisible: true,
			Capabilities: []string{
				pkgplugins.CapabilityMemory,
			},
		})
		host.Registry().RegisterMemory(pkgplugins.MemoryRegistration{
			PluginID: "memory/simple",
			Name:     "simple",
			Build: func(ctx context.Context, build pkgplugins.MemoryContext) (memory.Provider, error) {
				if build.DB == nil {
					return nil, errors.New("simple plugin requires a shared DB connection")
				}
				return New(build.DB), nil
			},
		})
	}))
}
