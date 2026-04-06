package lcm

import (
	"context"
	"errors"

	"github.com/vaayne/anna/pkg/memory"
	pluginmemory "github.com/vaayne/anna/plugins/memory"
)

func init() {
	pluginmemory.Register("lcm", pluginmemory.Registration{
		Meta: pluginmemory.ProviderMeta{
			Name:        "Lossless Context Management",
			Description: "Hierarchical summarisation with full history preservation",
			Capabilities: []string{
				"compactor", "searcher", "explorer",
				"profile", "sessions", "review",
			},
		},
		Factory: func(ctx context.Context, bc pluginmemory.BuildContext) (memory.Provider, error) {
			if bc.DB == nil {
				return nil, errors.New("lcm plugin requires a shared DB connection")
			}
			return New(bc.DB, bc.SummarizerFn, bc.Config)
		},
	})
}
