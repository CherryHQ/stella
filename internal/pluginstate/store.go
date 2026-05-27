package pluginstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/CherryHQ/stella/internal/orgctx"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type Store struct {
	q *sqlc.Queries
}

func New(db *sql.DB) *Store {
	return &Store{q: sqlc.New(db)}
}

func (s *Store) Get(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string) (map[string]any, bool, error) {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return nil, false, err
	}
	scope = scope.Normalize()
	raw, err := s.q.GetPluginStateEntry(ctx, sqlc.GetPluginStateEntryParams{
		PluginID:  pluginID,
		ScopeKind: scope.Kind,
		ScopeID:   scope.ID,
		StateKey:  key,
		OrgID:     sql.NullString{String: orgID, Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get plugin state %s/%s/%s/%s: %w", pluginID, scope.Kind, scope.ID, key, err)
	}
	value := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, false, fmt.Errorf("decode plugin state %s/%s/%s/%s: %w", pluginID, scope.Kind, scope.ID, key, err)
	}
	return cloneMap(value), true, nil
}

func (s *Store) Set(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string, value map[string]any) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	scope = scope.Normalize()
	encoded, err := json.Marshal(cloneMap(value))
	if err != nil {
		return fmt.Errorf("encode plugin state %s/%s/%s/%s: %w", pluginID, scope.Kind, scope.ID, key, err)
	}
	return s.q.UpsertPluginStateEntry(ctx, sqlc.UpsertPluginStateEntryParams{
		PluginID:  pluginID,
		ScopeKind: scope.Kind,
		ScopeID:   scope.ID,
		StateKey:  key,
		Value:     string(encoded),
		OrgID:     sql.NullString{String: orgID, Valid: true},
	})
}

func (s *Store) Delete(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string) error {
	orgID, err := requireOrgID(ctx)
	if err != nil {
		return err
	}
	scope = scope.Normalize()
	if err := s.q.DeletePluginStateEntry(ctx, sqlc.DeletePluginStateEntryParams{
		PluginID:  pluginID,
		ScopeKind: scope.Kind,
		ScopeID:   scope.ID,
		StateKey:  key,
		OrgID:     sql.NullString{String: orgID, Valid: true},
	}); err != nil {
		return fmt.Errorf("delete plugin state %s/%s/%s/%s: %w", pluginID, scope.Kind, scope.ID, key, err)
	}
	return nil
}

func requireOrgID(ctx context.Context) (string, error) {
	orgID := orgctx.OrgIDFromContext(ctx)
	if orgID == "" {
		return "", fmt.Errorf("org_id is required in context")
	}
	return orgID, nil
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	maps.Copy(out, src)
	return out
}
