package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func (s *Service) getDefinition(ctx context.Context, id string) (Definition, error) {
	row, err := s.q.GetPluginDefinition(ctx, id)
	if err != nil {
		return Definition{}, mapNotFound(err)
	}
	def := fromSQLDefinition(row)
	if def.Source == SourceBuiltin {
		if _, exists := s.catalog.Get(def.ID); !exists {
			return Definition{}, ErrNotFound
		}
	}
	if err := def.Validate(); err != nil {
		return Definition{}, err
	}
	return def, nil
}

func (s *Service) listDefinitions(ctx context.Context) ([]Definition, error) {
	rows, err := s.q.ListPluginDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	defs := make([]Definition, 0, len(rows))
	for _, row := range rows {
		defs = append(defs, fromSQLDefinition(row))
	}
	return defs, nil
}

func (s *Service) listConfigs(ctx context.Context, pluginID string, scope Scope, userID, agentID string) ([]Config, error) {
	rows, err := s.q.ListPluginConfigsOwned(ctx, sqlc.ListPluginConfigsOwnedParams{
		PluginID: pluginID, Scope: string(scope), UserID: nullableText(userID), AgentID: nullableText(agentID),
	})
	if err != nil {
		return nil, err
	}
	configs := make([]Config, 0, len(rows))
	for _, row := range rows {
		config := fromSQLConfig(row)
		if err := s.loadMCPServerChildren(ctx, &config); err != nil {
			return nil, err
		}
		configs = append(configs, config)
	}
	return configs, nil
}

func (s *Service) createConfig(ctx context.Context, config Config) (Config, error) {
	row, err := s.q.CreatePluginConfig(ctx, sqlc.CreatePluginConfigParams{
		ID: config.ID, PluginID: config.PluginID, Scope: string(config.Scope),
		UserID: nullableText(config.UserID), AgentID: nullableText(config.AgentID), Enabled: nullableBool(config.Enabled),
		Config: config.Payload, CredentialRefs: nonEmptyJSON(config.CredentialRefs), Revision: config.Revision,
	})
	if err != nil {
		return Config{}, mapConflict(err)
	}
	created := fromSQLConfig(row)
	if err := s.ensureMCPServerChildren(ctx, &created); err != nil {
		return Config{}, err
	}
	if err := s.loadMCPServerChildren(ctx, &created); err != nil {
		return Config{}, err
	}
	return created, nil
}

func (s *Service) updateConfigCAS(ctx context.Context, id string, revision int64, enabled *bool, payload, refs json.RawMessage) (Config, error) {
	row, err := s.q.UpdatePluginConfigCAS(ctx, sqlc.UpdatePluginConfigCASParams{
		ID: id, Revision: revision, Enabled: nullableBool(enabled), Config: payload, CredentialRefs: nonEmptyJSON(refs),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, ErrConflict
		}
		return Config{}, mapConflict(err)
	}
	updated := fromSQLConfig(row)
	if err := s.ensureMCPServerChildren(ctx, &updated); err != nil {
		return Config{}, err
	}
	if err := s.loadMCPServerChildren(ctx, &updated); err != nil {
		return Config{}, err
	}
	return updated, nil
}

func (s *Service) moveConfigCAS(ctx context.Context, id string, revision int64, scope Scope, userID, agentID string, enabled *bool, payload, refs json.RawMessage) (Config, error) {
	row, err := s.q.MovePluginConfigCAS(ctx, sqlc.MovePluginConfigCASParams{
		ID: id, Scope: string(scope), UserID: nullableText(userID), AgentID: nullableText(agentID),
		Enabled: nullableBool(enabled), Config: payload, CredentialRefs: nonEmptyJSON(refs), Revision: revision,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, ErrConflict
		}
		return Config{}, mapConflict(err)
	}
	updated := fromSQLConfig(row)
	if err := s.loadMCPServerChildren(ctx, &updated); err != nil {
		return Config{}, err
	}
	return updated, nil
}

func (s *Service) deleteConfigCAS(ctx context.Context, id string, revision int64, pluginID string) (bool, error) {
	rows, err := s.q.DeletePluginConfigCAS(ctx, sqlc.DeletePluginConfigCASParams{ID: id, Revision: revision, PluginID: pluginID})
	return rows == 1, err
}

func (s *Service) resetBuiltinConfig(ctx context.Context, id string, revision int64, pluginID string) (Config, error) {
	row, err := s.q.ResetBuiltinPluginConfig(ctx, sqlc.ResetBuiltinPluginConfigParams{ID: id, Revision: revision, PluginID: pluginID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, ErrConflict
		}
		return Config{}, mapConflict(err)
	}
	reset := fromSQLConfig(row)
	if err := s.loadMCPServerChildren(ctx, &reset); err != nil {
		return Config{}, err
	}
	return reset, nil
}

func (s *Service) createCustom(ctx context.Context, def Definition, config Config) (Definition, Config, error) {
	defRow, err := s.q.CreatePluginDefinition(ctx, sqlc.CreatePluginDefinitionParams{
		ID: def.ID, DisplayName: def.DisplayName, Source: string(def.Source),
		Spec: def.Spec, DefaultEnabled: def.DefaultEnabled, Revision: def.Revision, CreatorUserID: nullableText(def.CreatorUserID),
	})
	if err != nil {
		return Definition{}, Config{}, mapConflict(err)
	}
	configRow, err := s.q.CreatePluginConfig(ctx, sqlc.CreatePluginConfigParams{
		ID: config.ID, PluginID: config.PluginID, Scope: string(config.Scope), UserID: nullableText(config.UserID), AgentID: nullableText(config.AgentID),
		Enabled: nullableBool(config.Enabled), Config: config.Payload, CredentialRefs: nonEmptyJSON(config.CredentialRefs), Revision: config.Revision,
	})
	if err != nil {
		return Definition{}, Config{}, mapConflict(err)
	}
	created := fromSQLConfig(configRow)
	if err := s.ensureMCPServerChildren(ctx, &created); err != nil {
		return Definition{}, Config{}, err
	}
	if err := s.loadMCPServerChildren(ctx, &created); err != nil {
		return Definition{}, Config{}, err
	}
	return fromSQLDefinition(defRow), created, nil
}

func nullableText(value string) pgtype.Text { return pgtype.Text{String: value, Valid: value != ""} }

func nullableBool(value *bool) pgtype.Bool {
	if value == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *value, Valid: true}
}

func boolValue(value pgtype.Bool) *bool {
	if !value.Valid {
		return nil
	}
	result := value.Bool
	return &result
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nonEmptyJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func fromSQLDefinition(row sqlc.PluginDefinition) Definition {
	return Definition{ID: row.ID, DisplayName: row.DisplayName, Source: Source(row.Source), Spec: row.Spec, DefaultEnabled: row.DefaultEnabled, Revision: row.Revision, CreatorUserID: textValue(row.CreatorUserID), CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
}

func fromSQLConfig(row sqlc.PluginConfig) Config {
	return Config{ID: row.ID, PluginID: row.PluginID, Scope: Scope(row.Scope), UserID: textValue(row.UserID), AgentID: textValue(row.AgentID), Enabled: boolValue(row.Enabled), Payload: row.Config, CredentialRefs: row.CredentialRefs, Revision: row.Revision, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
}

func (s *Service) loadMCPServerChildren(ctx context.Context, config *Config) error {
	return loadMCPServerChildren(ctx, s.q, config)
}

func loadMCPServerChildren(ctx context.Context, q *sqlc.Queries, config *Config) error {
	if config == nil || config.ID == "" {
		return nil
	}
	rows, err := q.ListPluginConfigMCPServersForConfig(ctx, config.ID)
	if err != nil {
		return err
	}
	config.MCPServers = make([]MCPServerChild, 0, len(rows))
	for _, row := range rows {
		config.MCPServers = append(config.MCPServers, MCPServerChild{ID: row.ID, ParentConfigID: row.ConfigID, ServerKey: row.ServerKey, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()})
	}
	return nil
}

func (s *Service) ensureMCPServerChildren(ctx context.Context, config *Config) error {
	if config == nil || config.ID == "" || len(config.Payload) == 0 {
		return nil
	}
	definition, err := s.q.GetPluginDefinition(ctx, config.PluginID)
	if err != nil {
		return err
	}
	resolved, err := mergeObjects(definition.Spec, config.Payload)
	if err != nil {
		return err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(resolved, &payload); err != nil {
		return fmt.Errorf("plugin: decode config payload: %w", err)
	}
	raw, ok := payload["mcp_servers"]
	if !ok {
		return nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &servers); err != nil || servers == nil {
		return fmt.Errorf("plugin: mcp_servers must be an object")
	}
	persisted, err := s.q.ListPluginConfigMCPServersForConfig(ctx, config.ID)
	if err != nil {
		return err
	}
	persistedKeys := make(map[string]struct{}, len(persisted))
	for _, row := range persisted {
		persistedKeys[row.ServerKey] = struct{}{}
	}
	for key := range servers {
		if _, exists := persistedKeys[key]; exists {
			continue
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		row, err := s.q.CreatePluginConfigMCPServer(ctx, sqlc.CreatePluginConfigMCPServerParams{ID: id.String(), ConfigID: config.ID, ServerKey: key})
		if err != nil {
			return mapConflict(err)
		}
		config.MCPServers = append(config.MCPServers, MCPServerChild{ID: row.ID, ParentConfigID: row.ConfigID, ServerKey: row.ServerKey, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()})
	}
	return nil
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func mapConflict(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23505") {
		return ErrConflict
	}
	return err
}
