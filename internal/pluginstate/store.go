package pluginstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type Store struct {
	db *pgxpool.Pool
	q  *sqlc.Queries
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db, q: sqlc.New(db)}
}

func (s *Store) Get(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string) (map[string]any, bool, error) {
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
	return cloneMap(value), true, nil
}

func (s *Store) Set(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string, value map[string]any) error {
	scope = scope.Normalize()
	encoded, err := json.Marshal(cloneMap(value))
	if err != nil {
		return fmt.Errorf("encode plugin state %s/%s/%s/%s: %w", pluginID, scope.Kind, scope.ID, key, err)
	}
	return agentrun.WriteTx(ctx, s.db, func(q *sqlc.Queries) error {
		return q.UpsertPluginStateEntry(ctx, sqlc.UpsertPluginStateEntryParams{
			PluginID:  pluginID,
			ScopeKind: scope.Kind,
			ScopeID:   scope.ID,
			StateKey:  key,
			Value:     encoded,
		})
	})
}

func (s *Store) Delete(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string) error {
	scope = scope.Normalize()
	if err := agentrun.WriteTx(ctx, s.db, func(q *sqlc.Queries) error {
		return q.DeletePluginStateEntry(ctx, sqlc.DeletePluginStateEntryParams{
			PluginID:  pluginID,
			ScopeKind: scope.Kind,
			ScopeID:   scope.ID,
			StateKey:  key,
		})
	}); err != nil {
		return fmt.Errorf("delete plugin state %s/%s/%s/%s: %w", pluginID, scope.Kind, scope.ID, key, err)
	}
	return nil
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	maps.Copy(out, src)
	return out
}
