package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// LegacyService is the migration-only bridge used while upgrading
// installations that still have the pre-filesystem plugin rows. It exposes no
// steady-state resource CRUD and must not be constructed after the migration
// marker is complete.
type LegacyService struct {
	db                 *pgxpool.Pool
	q                  *sqlc.Queries
	catalog            *Catalog
	contentStore       *ContentStore
	builtinSkillReader BuiltinSkillReader
}

// BuiltinSkillReader reads release-owned Skill files while materializing the
// legacy catalog into the filesystem. The callback is used only before the
// filesystem migration marker is committed.
type BuiltinSkillReader func(context.Context, string, string) (map[string][]byte, map[string]fs.FileMode, error)

// NewLegacyService constructs the migration-only plugin reader.
func NewLegacyService(db *pgxpool.Pool, catalog *Catalog, contentStore *ContentStore, builtinSkillReader BuiltinSkillReader) *LegacyService {
	if catalog == nil {
		catalog = NewCatalog()
	}
	return &LegacyService{db: db, q: sqlc.New(db), catalog: catalog, contentStore: contentStore, builtinSkillReader: builtinSkillReader}
}

// SyncBuiltinDefaults materializes shipped definitions and their system config
// rows before the one-way export. It is intentionally available only on the
// migration bridge, rather than on the runtime resource API.
func (s *LegacyService) SyncBuiltinDefaults(ctx context.Context) error {
	if s == nil || s.db == nil || ctx == nil {
		return ErrLegacyMigrationConflict
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	if err := q.LockPluginCatalog(ctx); err != nil {
		return err
	}
	for _, def := range s.catalog.Definitions() {
		if def.Source != SourceBuiltin {
			continue
		}
		if err := def.Validate(); err != nil {
			return err
		}
		if _, err := q.UpsertPluginDefinition(ctx, sqlc.UpsertPluginDefinitionParams{
			ID: def.ID, DisplayName: def.DisplayName,
			Source: string(def.Source), Spec: def.Spec, DefaultEnabled: def.DefaultEnabled, Revision: def.Revision,
		}); err != nil {
			return fmt.Errorf("sync definition %s: %w", def.ID, err)
		}
		row, err := q.EnsureSystemPluginConfig(ctx, sqlc.EnsureSystemPluginConfigParams{PluginID: def.ID, Config: json.RawMessage(`{}`)})
		if err != nil {
			return fmt.Errorf("sync config %s: %w", def.ID, err)
		}
		var declaration ResourcePayload
		if err := json.Unmarshal(def.Spec, &declaration); err != nil {
			return fmt.Errorf("sync definition %s: decode spec: %w", def.ID, err)
		}
		if declaration.Origin != "remote_mcp" && len(declaration.MCPServers) != 0 {
			config := fromSQLConfig(sqlc.PluginConfig(row))
			if _, resolveErr := MergeDefinitionConfig(def.Spec, config.Payload); resolveErr != nil {
				continue
			}
			if err := ensureMCPServerChildren(ctx, q, &config); err != nil {
				return fmt.Errorf("sync MCP children %s: %w", def.ID, err)
			}
		}
	}
	return classifyCommitError(tx.Commit(ctx))
}
