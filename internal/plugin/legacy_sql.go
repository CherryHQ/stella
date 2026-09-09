package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

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
	retiredAt := time.Time{}
	if row.RetiredAt.Valid {
		retiredAt = row.RetiredAt.Time.UTC()
	}
	return Definition{ID: row.ID, DisplayName: row.DisplayName, Source: Source(row.Source), Spec: row.Spec, DefaultEnabled: row.DefaultEnabled, Revision: row.Revision, CreatorUserID: textValue(row.CreatorUserID), RetiredAt: retiredAt, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
}

func fromSQLConfig(row sqlc.PluginConfig) Config {
	return Config{ID: row.ID, PluginID: row.PluginID, Scope: Scope(row.Scope), UserID: textValue(row.UserID), AgentID: textValue(row.AgentID), Enabled: boolValue(row.Enabled), Payload: row.Config, CredentialRefs: row.CredentialRefs, Revision: row.Revision, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
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

func ensureMCPServerChildren(ctx context.Context, q *sqlc.Queries, config *Config) error {
	if config == nil || config.ID == "" || len(config.Payload) == 0 {
		return nil
	}
	definition, err := q.GetPluginDefinition(ctx, config.PluginID)
	if err != nil {
		return err
	}
	resolved, err := MergeDefinitionConfig(definition.Spec, config.Payload)
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
	persisted, err := q.ListPluginConfigMCPServersForConfig(ctx, config.ID)
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
		row, err := q.CreatePluginConfigMCPServer(ctx, sqlc.CreatePluginConfigMCPServerParams{ID: id.String(), ConfigID: config.ID, ServerKey: key})
		if err != nil {
			return mapConflict(err)
		}
		config.MCPServers = append(config.MCPServers, MCPServerChild{ID: row.ID, ParentConfigID: row.ConfigID, ServerKey: row.ServerKey, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()})
	}
	return nil
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
