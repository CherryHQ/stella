package reflect

import (
	"context"

	"github.com/CherryHQ/stella/internal/config"
)

// Store is the read-only subset of config.Store reflect needs to run a
// review cycle. Narrowed from config.Store so dispatcher tests can supply
// a fake without implementing the full config surface; satisfied by
// config.Store via structural typing.
type Store interface {
	ListEnabledAgents(ctx context.Context) ([]config.Agent, error)
	Snapshot(ctx context.Context, agentID string) (*config.Snapshot, error)
}
