package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// publishedStateMarkerKey is separate from plugin_cutover_v1. The latter
// proves the legacy rows were copied; this marker proves the copied definitions
// and configs have crossed the canonical Spec/Config boundary. Keeping the
// markers separate lets an interrupted startup retry the second phase without
// replaying legacy credential or child identities.
const publishedStateMarkerKey = "plugin_published_v1"

// MigratePublishedState upgrades rows that already live in the unified tables.
// It is deliberately one short transaction under the same catalog lock used by
// legacy cutover. Definition and config IDs, revisions, credentials, and MCP
// child rows are preserved. A failed transaction leaves no marker, so startup
// can retry after the offending row is repaired.
func MigratePublishedState(ctx context.Context, db *pgxpool.Pool, catalog *Catalog) error {
	if db == nil || catalog == nil {
		return ErrInvalidDefinition
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin published plugin migration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		return fmt.Errorf("set published plugin migration lock timeout: %w", err)
	}
	if err := sqlc.New(tx).LockPluginCatalog(ctx); err != nil {
		return fmt.Errorf("lock published plugin migration: %w", err)
	}
	var marker string
	err = tx.QueryRow(ctx, `SELECT value FROM app_setting WHERE key = $1`, publishedStateMarkerKey).Scan(&marker)
	if err == nil {
		if marker == "v1" {
			return ErrImportComplete
		}
		return fmt.Errorf("%w: unexpected published-state marker", ErrLegacyMigrationConflict)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read published-state marker: %w", err)
	}

	declarations, err := collectPublishedMCPDeclarations(ctx, tx)
	if err != nil {
		return err
	}
	definitions, err := migratePublishedDefinitions(ctx, tx, catalog, declarations)
	if err != nil {
		return err
	}
	if err := migratePublishedConfigs(ctx, tx, definitions); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO app_setting (key, value, updated_at)
		VALUES ($1, 'v1', now())
	`, publishedStateMarkerKey); err != nil {
		return fmt.Errorf("write published-state marker: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return classifyCommitError(err)
	}
	return nil
}

func migratePublishedDefinitions(ctx context.Context, tx pgx.Tx, catalog *Catalog, declarations map[string]map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	rows, err := tx.Query(ctx, `SELECT id, source, spec FROM plugin_definition ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read published definitions: %w", err)
	}
	defer rows.Close()
	type rowData struct {
		id, source string
		raw        json.RawMessage
	}
	var sourceRows []rowData
	for rows.Next() {
		var row rowData
		if err := rows.Scan(&row.id, &row.source, &row.raw); err != nil {
			return nil, fmt.Errorf("scan published definition: %w", err)
		}
		sourceRows = append(sourceRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read published definitions: %w", err)
	}
	rows.Close()
	definitions := make(map[string]json.RawMessage)
	for _, row := range sourceRows {
		canonical, err := canonicalPublishedSpec(row.id, Source(row.source), row.raw, catalog, declarations[row.id])
		if err != nil {
			return nil, err
		}
		definitions[row.id] = canonical
		if !bytes.Equal(bytes.TrimSpace(row.raw), bytes.TrimSpace(canonical)) {
			if _, err := tx.Exec(ctx, `UPDATE plugin_definition SET spec = $2 WHERE id = $1`, row.id, canonical); err != nil {
				return nil, fmt.Errorf("publish definition %s: %w", row.id, err)
			}
		}
	}
	return definitions, nil
}

// collectPublishedMCPDeclarations gathers resource declarations before the
// definition pass. Older unified rows sometimes published an empty spec and
// kept the complete MCP endpoint only in each config; publishing the union of
// those authored keys makes every parameter map addressable without borrowing
// a connection from another config.
func collectPublishedMCPDeclarations(ctx context.Context, tx pgx.Tx) (map[string]map[string]json.RawMessage, error) {
	childRows, err := tx.Query(ctx, `SELECT config_id, server_key FROM plugin_config_mcp_server ORDER BY config_id, server_key`)
	if err != nil {
		return nil, fmt.Errorf("read MCP child identities: %w", err)
	}
	children := make(map[string]map[string]struct{})
	for childRows.Next() {
		var configID, key string
		if err := childRows.Scan(&configID, &key); err != nil {
			childRows.Close()
			return nil, fmt.Errorf("scan MCP child identity: %w", err)
		}
		if children[configID] == nil {
			children[configID] = make(map[string]struct{})
		}
		children[configID][key] = struct{}{}
	}
	if err := childRows.Err(); err != nil {
		childRows.Close()
		return nil, fmt.Errorf("read MCP child identities: %w", err)
	}
	childRows.Close()
	rows, err := tx.Query(ctx, `SELECT id, plugin_id, config FROM plugin_config WHERE config IS NOT NULL ORDER BY plugin_id, id`)
	if err != nil {
		return nil, fmt.Errorf("read MCP declarations from configs: %w", err)
	}
	defer rows.Close()
	result := make(map[string]map[string]json.RawMessage)
	for rows.Next() {
		var configID, pluginID string
		var raw json.RawMessage
		if err := rows.Scan(&configID, &pluginID, &raw); err != nil {
			return nil, fmt.Errorf("scan MCP declaration config: %w", err)
		}
		declarations, err := legacyMCPDeclarations(raw)
		if err != nil {
			return nil, fmt.Errorf("config %s MCP declaration: %w", pluginID, err)
		}
		if len(declarations) == 0 {
			continue
		}
		// Nested MCP maps are already keyed by the durable child identity. Do
		// not invent a new declaration for a key whose child row is absent;
		// migration must fail on that dangling config instead of making it
		// executable by silently expanding the shared definition.
		object, err := decodeSpecObject(raw)
		if err != nil {
			return nil, fmt.Errorf("config %s MCP declaration: %w", configID, err)
		}
		if _, nested := object["mcp_servers"]; nested {
			allowed := children[configID]
			for key := range declarations {
				if _, ok := allowed[key]; !ok {
					delete(declarations, key)
				}
			}
			if len(declarations) == 0 {
				continue
			}
		}
		byKey := result[pluginID]
		if byKey == nil {
			byKey = make(map[string]json.RawMessage)
			result[pluginID] = byKey
		}
		for key, declaration := range declarations {
			if existing, exists := byKey[key]; exists {
				merged, err := mergeMCPDeclarationFields(existing, declaration)
				if err != nil {
					return nil, fmt.Errorf("config %s MCP server %s: %w", pluginID, key, err)
				}
				byKey[key] = merged
			} else {
				byKey[key] = bytes.Clone(declaration)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read MCP declarations from configs: %w", err)
	}
	return result, nil
}

func mergeMCPDeclarationFields(existing, incoming json.RawMessage) (json.RawMessage, error) {
	left, err := decodeSpecObject(existing)
	if err != nil {
		return nil, err
	}
	right, err := decodeSpecObject(incoming)
	if err != nil {
		return nil, err
	}
	for key, value := range right {
		if _, ok := left[key]; !ok {
			left[key] = bytes.Clone(value)
		}
	}
	return json.Marshal(left)
}

func legacyMCPDeclarations(raw json.RawMessage) (map[string]json.RawMessage, error) {
	object, err := decodeSpecObject(raw)
	if err != nil {
		return nil, fmt.Errorf("payload must be an object")
	}
	if nested, ok := object["mcp_servers"]; ok {
		servers, err := decodeSpecObject(nested)
		if err != nil {
			return nil, fmt.Errorf("mcp_servers must be an object")
		}
		result := make(map[string]json.RawMessage, len(servers))
		for key, child := range servers {
			if key == "" {
				return nil, fmt.Errorf("mcp server key must not be empty")
			}
			if _, err := decodeSpecObject(child); err != nil {
				return nil, fmt.Errorf("mcp server %q must be an object", key)
			}
			result[key] = bytes.Clone(child)
		}
		return result, nil
	}
	flat := make(map[string]json.RawMessage)
	hasConnection := false
	for _, field := range []string{"url", "transport", "auth_type", "credential_mode", "headers", "metadata"} {
		if value, ok := object[field]; ok {
			flat[field] = bytes.Clone(value)
			hasConnection = true
		}
	}
	if !hasConnection {
		return nil, nil
	}
	if value, ok := object["description"]; ok {
		flat["description"] = bytes.Clone(value)
	}
	return map[string]json.RawMessage{"main": mustMarshalRaw(flat)}, nil
}

// normalizeLegacyOAuthDeclaration converts the old provider shortcut into the
// formal requirement/binding representation before strict resource decoding.
// It is intentionally migration-only: a conflicting provider or binding is a
// data error, never a reason to silently discard a credential declaration.
func normalizeLegacyOAuthDeclaration(raw json.RawMessage) (json.RawMessage, error) {
	object, err := decodeSpecObject(raw)
	if err != nil {
		return nil, err
	}
	provider := ""
	if value, ok := object["oauth_provider"]; ok {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(object, "oauth_provider")
		} else if err := json.Unmarshal(value, &provider); err != nil || provider == "" {
			return nil, fmt.Errorf("oauth_provider must be a non-empty string")
		} else {
			delete(object, "oauth_provider")
		}
	}
	explicitProvider := provider != ""
	var requirements []OAuthRequirement
	if value, ok := object["oauth"]; ok {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(object, "oauth")
		} else if err := decodeStrictJSON(value, &requirements); err != nil {
			return nil, fmt.Errorf("oauth requirements are invalid: %w", err)
		}
	}
	find := func(name string) int {
		for i := range requirements {
			if requirements[i].Provider == name {
				return i
			}
		}
		return -1
	}
	if provider != "" {
		if len(requirements) != 0 && find(provider) < 0 {
			return nil, fmt.Errorf("oauth_provider %q conflicts with formal OAuth requirements", provider)
		}
		if find(provider) < 0 {
			requirements = append(requirements, OAuthRequirement{Provider: provider})
		}
	}
	if value, ok := object["session_env"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		var envs []SessionEnvResource
		if err := decodeStrictJSON(value, &envs); err == nil {
			for _, env := range envs {
				credential, ok := strings.CutPrefix(env.Source, "oauth.")
				if !ok {
					continue
				}
				bindingProvider := ""
				for _, requirement := range requirements {
					for _, binding := range requirement.Bindings {
						if binding.EnvVar != env.EnvVar {
							continue
						}
						if binding.Credential != credential {
							return nil, fmt.Errorf("OAuth session env %q conflicts with an existing binding", env.EnvVar)
						}
						if bindingProvider != "" && bindingProvider != requirement.Provider {
							return nil, fmt.Errorf("OAuth session env %q has multiple providers", env.EnvVar)
						}
						bindingProvider = requirement.Provider
					}
				}
				if bindingProvider != "" {
					if explicitProvider && provider != bindingProvider {
						return nil, fmt.Errorf("oauth_provider %q conflicts with an existing binding", provider)
					}
					provider = bindingProvider
				} else if !explicitProvider {
					if len(requirements) == 1 {
						provider = requirements[0].Provider
					} else {
						return nil, fmt.Errorf("OAuth session env %q has no unambiguous provider", env.EnvVar)
					}
				}
				index := find(provider)
				if index < 0 {
					requirements = append(requirements, OAuthRequirement{Provider: provider})
					index = len(requirements) - 1
				}
				binding := OAuthBinding{Credential: credential, EnvVar: env.EnvVar}
				found := false
				for _, existing := range requirements[index].Bindings {
					if existing.EnvVar == binding.EnvVar {
						if existing.Credential != binding.Credential {
							return nil, fmt.Errorf("OAuth session env %q conflicts with an existing binding", env.EnvVar)
						}
						found = true
					}
				}
				if !found {
					requirements[index].Bindings = append(requirements[index].Bindings, binding)
				}
			}
		}
	} else if ok {
		delete(object, "session_env")
	}
	if len(requirements) != 0 {
		encoded, err := json.Marshal(requirements)
		if err != nil {
			return nil, err
		}
		object["oauth"] = encoded
	}
	return json.Marshal(object)
}

func augmentMCPDefinitionSpec(raw json.RawMessage, inferred map[string]json.RawMessage) (json.RawMessage, error) {
	object, err := decodeSpecObject(raw)
	if err != nil {
		return nil, err
	}
	servers := make(map[string]json.RawMessage)
	if nested, ok := object["mcp_servers"]; ok {
		servers, err = decodeSpecObject(nested)
		if err != nil {
			return nil, fmt.Errorf("mcp_servers must be an object")
		}
	} else {
		flat := make(map[string]json.RawMessage)
		for _, field := range []string{"url", "transport", "auth_type", "credential_mode", "headers", "metadata"} {
			if value, ok := object[field]; ok {
				flat[field] = value
				delete(object, field)
			}
		}
		if len(flat) != 0 {
			servers["main"] = mustMarshalRaw(flat)
		}
	}
	for key, declaration := range inferred {
		if current, ok := servers[key]; !ok {
			servers[key] = bytes.Clone(declaration)
		} else {
			merged, err := mergeMCPDeclarationFields(current, declaration)
			if err != nil {
				return nil, fmt.Errorf("MCP server %q: %w", key, err)
			}
			servers[key] = merged
		}
	}
	if len(servers) != 0 {
		object["mcp_servers"] = mustMarshalRaw(servers)
	}
	return json.Marshal(object)
}

func canonicalPublishedSpec(id string, source Source, raw json.RawMessage, catalog *Catalog, inferred map[string]json.RawMessage) (json.RawMessage, error) {
	if source == SourceBuiltin {
		if shipped, ok := catalog.Get(id); ok {
			if err := shipped.Validate(); err != nil {
				return nil, fmt.Errorf("builtin definition %s: %w", id, err)
			}
			return bytes.Clone(shipped.Spec), nil
		}
	}
	originalObject, err := decodeSpecObject(raw)
	if err != nil {
		return nil, fmt.Errorf("definition %s: %w", id, err)
	}
	_, hadDigest := originalObject[contentDigestField]
	if hadDigest {
		if err := ValidateDefinitionSpecDigest(raw); err != nil {
			return nil, fmt.Errorf("definition %s has invalid content digest: %w", id, err)
		}
	}
	prepared := bytes.Clone(raw)
	prepared, err = normalizeLegacyOAuthDeclaration(prepared)
	if err != nil {
		return nil, fmt.Errorf("definition %s OAuth declaration: %w", id, err)
	}
	if len(inferred) != 0 {
		prepared, err = augmentMCPDefinitionSpec(prepared, inferred)
		if err != nil {
			return nil, fmt.Errorf("definition %s: %w", id, err)
		}
	}
	if ValidateDefinitionSpecDigest(prepared) == nil {
		if _, err := DecodeResourcePayload(prepared, "definition "+id); err != nil {
			return nil, fmt.Errorf("definition %s: %w", id, err)
		}
		return prepared, nil
	}
	// Legacy unified rows may have the resource declaration but no digest. The
	// only tolerated compatibility here is declaration normalization; old flat
	// config values are handled separately below. Unknown fields are rejected so
	// an arbitrary historical blob cannot become executable merely by hashing it.
	object, err := decodeSpecObject(prepared)
	if err != nil {
		return nil, fmt.Errorf("definition %s: %w", id, err)
	}
	delete(object, contentDigestField)
	delete(object, "name")
	delete(object, "schema")
	if _, ok := object["mcp_servers"]; !ok {
		flat := make(map[string]json.RawMessage)
		for _, field := range []string{"url", "transport", "auth_type", "credential_mode", "headers", "metadata"} {
			if value, exists := object[field]; exists {
				flat[field] = value
				delete(object, field)
			}
		}
		if len(flat) > 0 {
			encoded, err := json.Marshal(flat)
			if err != nil {
				return nil, err
			}
			object["mcp_servers"] = json.RawMessage(`{"main":` + string(encoded) + `}`)
		}
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode definition %s: %w", id, err)
	}
	if _, err := DecodeResourcePayload(encoded, "definition "+id); err != nil {
		return nil, fmt.Errorf("definition %s: %w", id, err)
	}
	canonical, err := PublishDefinitionSpec(encoded)
	if err != nil {
		return nil, fmt.Errorf("publish definition %s: %w", id, err)
	}
	return canonical, nil
}

func migratePublishedConfigs(ctx context.Context, tx pgx.Tx, definitions map[string]json.RawMessage) error {
	rows, err := tx.Query(ctx, `SELECT id, plugin_id, config, credential_refs FROM plugin_config ORDER BY id`)
	if err != nil {
		return fmt.Errorf("read published configs: %w", err)
	}
	defer rows.Close()
	type rowData struct {
		id, pluginID string
		raw, refs    json.RawMessage
	}
	var sourceRows []rowData
	for rows.Next() {
		var row rowData
		if err := rows.Scan(&row.id, &row.pluginID, &row.raw, &row.refs); err != nil {
			return fmt.Errorf("scan published config: %w", err)
		}
		sourceRows = append(sourceRows, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read published configs: %w", err)
	}
	rows.Close()
	for _, row := range sourceRows {
		if len(row.raw) == 0 || bytes.Equal(bytes.TrimSpace(row.raw), []byte("null")) {
			// A disabled negative config intentionally has no parameter object.
			// Keep SQL NULL and its empty credential refs untouched.
			continue
		}
		spec, ok := definitions[row.pluginID]
		if !ok {
			return fmt.Errorf("%w: config %s references missing definition %s", ErrLegacyMigrationConflict, row.id, row.pluginID)
		}
		prepared, err := normalizeLegacyOAuthDeclaration(row.raw)
		if err != nil {
			return fmt.Errorf("migrate config %s OAuth declaration: %w", row.id, err)
		}
		prepared, err = normalizeLegacyMCPConfigForChildren(ctx, tx, row.id, spec, prepared)
		if err != nil {
			return fmt.Errorf("migrate config %s MCP declaration: %w", row.id, err)
		}
		canonical, err := normalizeImportedConfigPayload(spec, prepared)
		if err != nil {
			return fmt.Errorf("migrate config %s: %w", row.id, err)
		}
		if _, err := MergeDefinitionConfig(spec, canonical); err != nil {
			return fmt.Errorf("migrate config %s parameters: %w", row.id, err)
		}
		if !bytes.Equal(bytes.TrimSpace(row.raw), bytes.TrimSpace(canonical)) {
			if _, err := tx.Exec(ctx, `UPDATE plugin_config SET config = $2 WHERE id = $1`, row.id, canonical); err != nil {
				return fmt.Errorf("migrate config %s: %w", row.id, err)
			}
		}
		refs, err := normalizePublishedCredentialRefs(spec, canonical, row.refs)
		if err != nil {
			return fmt.Errorf("migrate config %s credential refs: %w", row.id, err)
		}
		if !bytes.Equal(bytes.TrimSpace(row.refs), bytes.TrimSpace(refs)) {
			if _, err := tx.Exec(ctx, `UPDATE plugin_config SET credential_refs = $2 WHERE id = $1`, row.id, refs); err != nil {
				return fmt.Errorf("migrate config %s credential refs: %w", row.id, err)
			}
		}
		if err := ensurePublishedMCPServerChildren(ctx, tx, row.id, spec, canonical); err != nil {
			return fmt.Errorf("ensure MCP children for config %s: %w", row.id, err)
		}
	}
	return nil
}

func normalizeLegacyMCPConfigForChildren(ctx context.Context, tx pgx.Tx, configID string, spec, raw json.RawMessage) (json.RawMessage, error) {
	object, err := decodeSpecObject(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := object["mcp_servers"]; ok {
		return raw, nil
	}
	flat := make(map[string]json.RawMessage)
	hasConnection := false
	for _, field := range []string{"url", "transport", "auth_type", "credential_mode", "headers", "metadata"} {
		if value, ok := object[field]; ok {
			flat[field] = value
			hasConnection = true
			delete(object, field)
		}
	}
	if !hasConnection {
		return raw, nil
	}
	if value, ok := object["description"]; ok {
		flat["description"] = value
		delete(object, "description")
	}
	rows, err := tx.Query(ctx, `SELECT server_key FROM plugin_config_mcp_server WHERE config_id = $1 ORDER BY server_key`, configID)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(keys) == 0 {
		declared, err := publishedMCPKeys(spec, nil)
		if err != nil {
			return nil, err
		}
		for key := range declared {
			keys = append(keys, key)
		}
	}
	if len(keys) != 1 {
		return nil, fmt.Errorf("flat MCP config requires exactly one existing server key")
	}
	object["mcp_servers"] = mustMarshalRaw(map[string]json.RawMessage{keys[0]: mustMarshalRaw(flat)})
	return json.Marshal(object)
}

// normalizePublishedCredentialRefs moves the legacy one-config ref object
// under the named server map. Existing locators are copied byte-for-byte; the
// migration never derives a new Vault name from a parent when a child ref is
// already present.
func normalizePublishedCredentialRefs(spec, config, raw json.RawMessage) (json.RawMessage, error) {
	keys, err := publishedMCPKeys(spec, config)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return bytes.Clone(raw), nil
	}
	authTypes, err := publishedMCPAuthTypes(spec, config)
	if err != nil {
		return nil, err
	}
	object, err := decodeSpecObject(raw)
	if err != nil {
		return nil, fmt.Errorf("credential refs must be an object")
	}
	refs := make(map[string]json.RawMessage, len(keys))
	if nested, ok := object["mcp_servers"]; ok {
		refs, err = decodeSpecObject(nested)
		if err != nil {
			return nil, fmt.Errorf("mcp credential refs must be an object")
		}
		for key := range refs {
			if _, expected := keys[key]; !expected {
				return nil, fmt.Errorf("credential refs contain unknown MCP server %q", key)
			}
		}
	} else {
		for key := range object {
			if key != "bearer" && key != "oauth_bundle" && key != "oauth_client_secret" {
				return nil, fmt.Errorf("credential refs contain unsupported field %q", key)
			}
		}
		if len(keys) != 1 {
			return nil, fmt.Errorf("flat credential refs require exactly one MCP server")
		}
		for key := range keys {
			refs[key] = mustMarshalRaw(object)
		}
	}
	for key := range keys {
		if _, ok := refs[key]; !ok {
			if authTypes[key] != "none" {
				return nil, fmt.Errorf("credential refs missing required credentials for MCP server %q", key)
			}
			refs[key] = json.RawMessage(`{}`)
		}
	}
	return json.Marshal(map[string]json.RawMessage{"mcp_servers": mustMarshalRaw(refs)})
}

func publishedMCPAuthTypes(spec, config json.RawMessage) (map[string]string, error) {
	merged, err := MergeDefinitionConfig(spec, config)
	if err != nil {
		return nil, err
	}
	object, err := decodeSpecObject(merged)
	if err != nil {
		return nil, err
	}
	nested, ok := object["mcp_servers"]
	if !ok {
		return map[string]string{}, nil
	}
	servers, err := decodeSpecObject(nested)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(servers))
	for key, raw := range servers {
		var server MCPServerResource
		if err := decodeStrictJSON(raw, &server); err != nil {
			return nil, err
		}
		result[key] = server.AuthType
	}
	return result, nil
}

func publishedMCPKeys(spec, config json.RawMessage) (map[string]json.RawMessage, error) {
	if len(config) == 0 {
		object, err := decodeSpecObject(spec)
		if err != nil {
			return nil, err
		}
		nested, ok := object["mcp_servers"]
		if !ok {
			return nil, nil
		}
		return decodeSpecObject(nested)
	}
	configObject, err := decodeSpecObject(config)
	if err != nil {
		return nil, err
	}
	if nested, ok := configObject["mcp_servers"]; ok {
		return decodeSpecObject(nested)
	}
	merged, err := MergeDefinitionConfig(spec, config)
	if err != nil {
		return nil, err
	}
	mergedObject, err := decodeSpecObject(merged)
	if err != nil {
		return nil, err
	}
	nested, ok := mergedObject["mcp_servers"]
	if !ok {
		return nil, nil
	}
	return decodeSpecObject(nested)
}

// The generic SQL store resolves children against the merged definition. A
// migration must use the config's authored key set instead, otherwise two
// configs with different servers would gain each other's child rows.
func ensurePublishedMCPServerChildren(ctx context.Context, tx pgx.Tx, configID string, spec, config json.RawMessage) error {
	keys, err := publishedMCPKeys(spec, config)
	if err != nil {
		return err
	}
	for key := range keys {
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_config_mcp_server (config_id, server_key)
			VALUES ($1, $2)
			ON CONFLICT (config_id, server_key) DO NOTHING
		`, configID, key); err != nil {
			return err
		}
	}
	return nil
}
