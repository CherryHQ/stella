package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type StateStore struct {
	q *sqlc.Queries
}

func NewStateStore(db *pgxpool.Pool) *StateStore {
	return &StateStore{q: sqlc.New(db)}
}

func (s *StateStore) Get(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string) (map[string]any, bool, error) {
	scope = scope.Normalize()
	raw, err := s.q.GetPluginStateEntry(ctx, sqlc.GetPluginStateEntryParams{
		PluginID:  pluginID,
		ScopeKind: scope.Kind,
		ScopeID:   scope.ID,
		StateKey:  key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get plugin state %s/%s/%s/%s: %w", pluginID, scope.Kind, scope.ID, key, err)
	}
	value := map[string]any{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false, fmt.Errorf("decode plugin state %s/%s/%s/%s: %w", pluginID, scope.Kind, scope.ID, key, err)
	}
	return cloneStateMap(value), true, nil
}

func (s *StateStore) Set(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string, value map[string]any) error {
	scope = scope.Normalize()
	encoded, err := json.Marshal(cloneStateMap(value))
	if err != nil {
		return fmt.Errorf("encode plugin state %s/%s/%s/%s: %w", pluginID, scope.Kind, scope.ID, key, err)
	}
	return s.q.UpsertPluginStateEntry(ctx, sqlc.UpsertPluginStateEntryParams{
		PluginID:  pluginID,
		ScopeKind: scope.Kind,
		ScopeID:   scope.ID,
		StateKey:  key,
		Value:     encoded,
	})
}

func (s *StateStore) Delete(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string) error {
	scope = scope.Normalize()
	if err := s.q.DeletePluginStateEntry(ctx, sqlc.DeletePluginStateEntryParams{
		PluginID:  pluginID,
		ScopeKind: scope.Kind,
		ScopeID:   scope.ID,
		StateKey:  key,
	}); err != nil {
		return fmt.Errorf("delete plugin state %s/%s/%s/%s: %w", pluginID, scope.Kind, scope.ID, key, err)
	}
	return nil
}

func cloneStateMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	maps.Copy(out, src)
	return out
}
