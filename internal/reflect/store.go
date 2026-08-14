package reflect

import (
	"context"

	"github.com/CherryHQ/stella/internal/config"
)

// Store lists the Agents Reflect reviews. Snapshot loading is a separate,
// credential-aware dependency.
type Store interface {
	ListEnabledAgents(ctx context.Context) ([]config.Agent, error)
}
