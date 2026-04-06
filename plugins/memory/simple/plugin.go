package simple

import (
	"context"
	"errors"

	"github.com/vaayne/anna/pkg/memory"
	pluginmemory "github.com/vaayne/anna/plugins/memory"
)

func init() {
	pluginmemory.Register("simple", pluginmemory.Registration{
		Meta: pluginmemory.ProviderMeta{
			Name:        "Simple Sliding Window",
			Description: "Sliding-window context with no compaction or summaries",
			Capabilities: []string{
				"profile", "sessions",
			},
		},
		Factory: func(ctx context.Context, bc pluginmemory.BuildContext) (memory.Provider, error) {
			if bc.DB == nil {
				return nil, errors.New("simple plugin requires a shared DB connection")
			}
			return New(bc.DB), nil
		},
	})
}
