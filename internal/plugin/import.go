package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	legacyMCPTransportHTTP = "streamable_http"
	legacyMCPTransportSSE  = "sse"
	legacyMCPAuthNone      = "none"
	legacyMCPAuthBearer    = "bearer"
	legacyMCPAuthOAuth     = "oauth"
	legacyMCPCredShared    = "shared"
	legacyMCPCredPerUser   = "per_user"
)

const cutoverMarkerKey = "plugin_cutover_v1"

var (
	ErrImportComplete          = errors.New("plugin: legacy state was already imported")
	ErrToolOverrideSchema      = errors.New("plugin: tool override cutover identity constraints are not ready")
	ErrLegacyPluginUnknown     = errors.New("plugin: legacy plugin has no trusted definition")
	ErrLegacyMigrationConflict = errors.New("plugin: legacy state cannot be imported without losing data")
)

// LegacyPlugin is the old generic plugin row. Config is an opaque JSON object;
// the importer never treats it as a definition when a trusted catalog entry
// exists.
type LegacyPlugin struct {
	ID      string
	Kind    string
	Name    string
	Enabled bool
	Config  json.RawMessage
}

// LegacyManifestOverride is the old manifest override row. Config may be a
// sparse patch ($sparse=true) or a pre-sparse full snapshot.
type LegacyManifestOverride struct {
	PluginID           string
	Enabled            *bool
	SessionEnvVaultKey string
	Config             string
}

// LegacyMCPRegistration is a secret-free snapshot of one mcp_server row.
// CredentialRef and OAuth references are locators only; secret values never
// enter this type or the target JSON.
type LegacyMCPRegistration struct {
	ID             string
	Scope          string
	UserID         string
	AgentID        string
	Name           string
	URL            string
	Transport      string
	AuthType       string
	CredentialRef  string
	Enabled        bool
	Metadata       map[string]any
	Status         string
	StatusError    string
	Tools          json.RawMessage
	CredentialMode string
}

// LegacyToolOverride is retained for the conversion helper. The target schema
// needs plugin_id/local_tool_name before an MCP row can be migrated safely.
type LegacyToolOverride struct {
	ID       string
	ToolName string
	Scope    string
	UserID   string
	AgentID  string
	Enabled  bool
}

// LegacySnapshot is read while the cutover transaction holds the advisory
// lock. It is also useful to normalize an offline snapshot for a maintenance
// preview without touching a database.
type LegacySnapshot struct {
	Plugins           []LegacyPlugin
	ManifestOverrides []LegacyManifestOverride
	MCP               []LegacyMCPRegistration
	ToolOverrides     []LegacyToolOverride
}

// ToolOverrideMigration identifies one exact target row. It is intentionally
// not written by this preview; the final dual-identity policy cutover is pending.
type ToolOverrideMigration struct {
	LegacyID  string
	OldName   string
	NewName   string
	PluginID  string
	ConfigID  string
	Namespace string
	LocalTool string
	Scope     Scope
	UserID    string
	AgentID   string
	Enabled   bool
}

// ImportPlan is the fully validated additive write set. Callers should log
// only its IDs and counts; payloads can contain private endpoints/metadata.
type ImportPlan struct {
	Definitions   []Definition
	Configs       []Config
	ToolOverrides []ToolOverrideMigration
	CoreOverrides []LegacyToolOverride
}

// NormalizeLegacySnapshot converts all supported old state into the new
// definition/config model. It does not mutate a catalog or a database. Any
// ambiguity is returned before a caller can write a marker.
func NormalizeLegacySnapshot(snapshot LegacySnapshot, catalog *Catalog) (ImportPlan, error) {
	if catalog == nil {
		return ImportPlan{}, ErrInvalidDefinition
	}

	defs := make(map[string]Definition)
	for _, def := range catalog.Definitions() {
		if err := def.Validate(); err != nil {
			return ImportPlan{}, fmt.Errorf("catalog %s: %w", def.ID, err)
		}
		defs[def.ID] = def
	}

	overrides := make(map[string]LegacyManifestOverride, len(snapshot.ManifestOverrides))
	legacyIDs := make(map[string]string)
	configIDs := make(map[string]string)
	for _, override := range snapshot.ManifestOverrides {
		if _, exists := overrides[override.PluginID]; exists {
			return ImportPlan{}, fmt.Errorf("%w: duplicate manifest override %q", ErrLegacyMigrationConflict, override.PluginID)
		}
		overrides[override.PluginID] = override
		if _, trusted := defs[override.PluginID]; trusted || override.Config == "" {
			continue
		}
		newID := legacyCustomDefinitionID(override.PluginID)
		def, err := customDefinitionFromManifest(newID, override.Config)
		if err != nil {
			return ImportPlan{}, err
		}
		if err := def.Validate(); err != nil {
			return ImportPlan{}, fmt.Errorf("legacy manifest %s: %w", override.PluginID, err)
		}
		defs[def.ID] = def
		legacyIDs[override.PluginID] = def.ID
		configIDs[def.ID] = strings.TrimPrefix(def.ID, "custom/")
	}

	configs := make(map[string]ConfigAccumulator)
	for _, legacy := range snapshot.Plugins {
		id := legacyIDs[legacyPluginID(legacy)]
		if id == "" {
			id = legacyPluginID(legacy)
		}
		def, ok := defs[id]
		if !ok {
			return ImportPlan{}, fmt.Errorf("%w: %s", ErrLegacyPluginUnknown, id)
		}
		acc := configs[id]
		if err := acc.addLegacyPlugin(def, legacy); err != nil {
			return ImportPlan{}, err
		}
		configs[id] = acc
	}

	for _, override := range snapshot.ManifestOverrides {
		pluginID := legacyIDs[override.PluginID]
		if pluginID == "" {
			pluginID = override.PluginID
		}
		def, ok := defs[pluginID]
		if !ok {
			return ImportPlan{}, fmt.Errorf("%w: %s", ErrLegacyPluginUnknown, override.PluginID)
		}
		acc := configs[pluginID]
		if err := acc.addManifestOverride(def, override, legacyIDs[override.PluginID] != ""); err != nil {
			return ImportPlan{}, err
		}
		configs[pluginID] = acc
	}

	for _, legacy := range snapshot.MCP {
		def, config, err := normalizeMCP(legacy)
		if err != nil {
			return ImportPlan{}, err
		}
		if err := def.Validate(); err != nil {
			return ImportPlan{}, fmt.Errorf("MCP %s: %w", legacy.ID, err)
		}
		if existing, exists := defs[def.ID]; exists {
			if existing.Namespace != def.Namespace || existing.Backend != def.Backend || existing.ImplementationKey != def.ImplementationKey {
				return ImportPlan{}, fmt.Errorf("%w: MCP definition %s", ErrLegacyMigrationConflict, def.ID)
			}
		} else {
			defs[def.ID] = def
		}
		configs[def.ID] = ConfigAccumulator{config: config, hasConfig: true}
	}

	plan := ImportPlan{Definitions: make([]Definition, 0, len(defs)), Configs: make([]Config, 0, len(configs))}
	for _, def := range defs {
		plan.Definitions = append(plan.Definitions, def)
	}
	for id, acc := range configs {
		if !acc.hasConfig {
			continue
		}
		config := acc.config
		if len(config.Payload) == 0 && config.Enabled == nil && len(config.CredentialRefs) == 0 {
			continue
		}
		if len(config.Payload) == 0 && config.Enabled != nil && *config.Enabled {
			config.Payload = json.RawMessage(`{}`)
		}
		if config.ID == "" {
			config.ID = configIDs[id]
			if config.ID == "" {
				config.ID = stableLegacyConfigID(id)
			}
		}
		if err := config.Validate(); err != nil {
			return ImportPlan{}, fmt.Errorf("legacy config %s: %w", id, err)
		}
		plan.Configs = append(plan.Configs, config)
	}
	if err := validateNamespaceOwners(plan.Configs, plan.Definitions); err != nil {
		return ImportPlan{}, err
	}

	for _, override := range snapshot.ToolOverrides {
		migration, err := ConvertLegacyToolOverride(override, snapshot.MCP)
		if err != nil {
			return ImportPlan{}, err
		}
		if migration.PluginID != "" {
			plan.ToolOverrides = append(plan.ToolOverrides, migration)
		} else {
			plan.CoreOverrides = append(plan.CoreOverrides, override)
		}
	}
	slices.SortFunc(plan.Definitions, func(a, b Definition) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(plan.Configs, func(a, b Config) int { return strings.Compare(a.PluginID, b.PluginID) })
	slices.SortFunc(plan.ToolOverrides, func(a, b ToolOverrideMigration) int { return strings.Compare(a.LegacyID, b.LegacyID) })
	slices.SortFunc(plan.CoreOverrides, func(a, b LegacyToolOverride) int { return strings.Compare(a.ID, b.ID) })
	return plan, nil
}

// PreviewLegacyImport reads and normalizes legacy state under the same
// advisory transaction lock that the eventual cutover will use. It deliberately
// performs no writes, including the durable marker. The final importer belongs
// to the cutover phase after all target identity columns exist.
func PreviewLegacyImport(ctx context.Context, db *pgxpool.Pool, catalog *Catalog) (ImportPlan, error) {
	if db == nil || catalog == nil {
		return ImportPlan{}, ErrInvalidDefinition
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return ImportPlan{}, fmt.Errorf("begin plugin cutover preview: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := sqlc.New(tx).LockPluginCatalog(ctx); err != nil {
		return ImportPlan{}, fmt.Errorf("lock plugin cutover preview: %w", err)
	}
	var marker string
	err = tx.QueryRow(ctx, `SELECT value FROM app_setting WHERE key = $1`, cutoverMarkerKey).Scan(&marker)
	if err == nil {
		if marker == "v1" {
			return ImportPlan{}, ErrImportComplete
		}
		return ImportPlan{}, fmt.Errorf("%w: unexpected cutover marker", ErrLegacyMigrationConflict)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ImportPlan{}, fmt.Errorf("read plugin cutover marker: %w", err)
	}

	snapshot, err := readLegacySnapshot(ctx, tx)
	if err != nil {
		return ImportPlan{}, err
	}
	plan, err := NormalizeLegacySnapshot(snapshot, catalog)
	if err != nil {
		return plan, err
	}
	if len(plan.ToolOverrides) > 0 {
		return plan, fmt.Errorf("%w: %d plugin override(s) require final dual-identity constraints", ErrToolOverrideSchema, len(plan.ToolOverrides))
	}
	return plan, nil
}

func readLegacySnapshot(ctx context.Context, tx pgx.Tx) (LegacySnapshot, error) {
	var snapshot LegacySnapshot
	rows, err := tx.Query(ctx, `SELECT id, kind, name, enabled, config FROM plugin ORDER BY id`)
	if err != nil {
		return LegacySnapshot{}, fmt.Errorf("read legacy plugins: %w", err)
	}
	for rows.Next() {
		var row LegacyPlugin
		if err := rows.Scan(&row.ID, &row.Kind, &row.Name, &row.Enabled, &row.Config); err != nil {
			rows.Close()
			return LegacySnapshot{}, fmt.Errorf("scan legacy plugin: %w", err)
		}
		snapshot.Plugins = append(snapshot.Plugins, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return LegacySnapshot{}, fmt.Errorf("read legacy plugins: %w", err)
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT plugin_id, enabled, session_env_vault_key, config FROM plugin_override ORDER BY plugin_id`)
	if err != nil {
		return LegacySnapshot{}, fmt.Errorf("read legacy manifest overrides: %w", err)
	}
	for rows.Next() {
		var row LegacyManifestOverride
		var enabled pgtype.Bool
		if err := rows.Scan(&row.PluginID, &enabled, &row.SessionEnvVaultKey, &row.Config); err != nil {
			rows.Close()
			return LegacySnapshot{}, fmt.Errorf("scan legacy manifest override: %w", err)
		}
		if enabled.Valid {
			row.Enabled = &enabled.Bool
		}
		snapshot.ManifestOverrides = append(snapshot.ManifestOverrides, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return LegacySnapshot{}, fmt.Errorf("read legacy manifest overrides: %w", err)
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT id, scope, user_id, agent_id, name, url, transport, auth_type,
		       credential_ref, enabled, metadata, status, status_error, tools, credential_mode
		FROM mcp_server ORDER BY id
	`)
	if err != nil {
		return LegacySnapshot{}, fmt.Errorf("read legacy MCP registrations: %w", err)
	}
	for rows.Next() {
		var row LegacyMCPRegistration
		var userID, agentID pgtype.Text
		if err := rows.Scan(&row.ID, &row.Scope, &userID, &agentID, &row.Name, &row.URL, &row.Transport, &row.AuthType,
			&row.CredentialRef, &row.Enabled, &row.Metadata, &row.Status, &row.StatusError, &row.Tools, &row.CredentialMode); err != nil {
			rows.Close()
			return LegacySnapshot{}, fmt.Errorf("scan legacy MCP registration: %w", err)
		}
		row.UserID, row.AgentID = textValue(userID), textValue(agentID)
		snapshot.MCP = append(snapshot.MCP, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return LegacySnapshot{}, fmt.Errorf("read legacy MCP registrations: %w", err)
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT id, tool_name, scope, user_id, agent_id, enabled FROM tool_override ORDER BY id`)
	if err != nil {
		return LegacySnapshot{}, fmt.Errorf("read legacy tool overrides: %w", err)
	}
	for rows.Next() {
		var row LegacyToolOverride
		var userID, agentID pgtype.Text
		if err := rows.Scan(&row.ID, &row.ToolName, &row.Scope, &userID, &agentID, &row.Enabled); err != nil {
			rows.Close()
			return LegacySnapshot{}, fmt.Errorf("scan legacy tool override: %w", err)
		}
		row.UserID, row.AgentID = textValue(userID), textValue(agentID)
		snapshot.ToolOverrides = append(snapshot.ToolOverrides, row)
	}
	if err := rows.Err(); err != nil {
		return LegacySnapshot{}, fmt.Errorf("read legacy tool overrides: %w", err)
	}
	return snapshot, nil
}

func legacyPluginID(row LegacyPlugin) string {
	if row.ID != "" {
		return row.ID
	}
	return row.Kind + "/" + row.Name
}

func stableLegacyConfigID(pluginID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("stella://plugin-config/"+pluginID)).String()
}

func legacyCustomDefinitionID(oldID string) string {
	return "custom/" + stableLegacyConfigID("legacy-definition/"+oldID)
}

type namespaceOwner struct {
	namespace string
	scope     Scope
	userID    string
	agentID   string
}

func validateNamespaceOwners(configs []Config, definitions []Definition) error {
	claims := slices.Clone(configs)
	systemOverrides := make(map[string]bool)
	for _, config := range configs {
		if config.Scope == ScopeSystem {
			systemOverrides[config.PluginID] = true
		}
	}
	// Missing builtin rows will acquire a payload-bearing system projection.
	// These are conflict-check claims only, never fabricated persistent UUIDs.
	for _, def := range definitions {
		if def.Source == SourceBuiltin && !systemOverrides[def.ID] {
			claims = append(claims, Config{PluginID: def.ID, Namespace: def.Namespace, Scope: ScopeSystem, Payload: json.RawMessage(`{}`)})
		}
	}
	owners := make(map[namespaceOwner]string, len(claims))
	for _, config := range claims {
		if len(config.Payload) == 0 {
			continue
		}
		key := namespaceOwner{namespace: config.Namespace, scope: config.Scope, userID: config.UserID, agentID: config.AgentID}
		if prior, exists := owners[key]; exists && prior != config.PluginID {
			return fmt.Errorf("%w: namespace %q is claimed by %s and %s for %s/%s/%s", ErrLegacyMigrationConflict, config.Namespace, prior, config.PluginID, config.Scope, config.UserID, config.AgentID)
		}
		owners[key] = config.PluginID
	}
	return nil
}

type ConfigAccumulator struct {
	config    Config
	hasConfig bool
}

func (a *ConfigAccumulator) addLegacyPlugin(def Definition, row LegacyPlugin) error {
	if !a.hasConfig {
		a.config = Config{PluginID: def.ID, Namespace: def.Namespace, Scope: ScopeSystem, Revision: 1}
		a.hasConfig = true
	}
	if a.config.Enabled != nil && *a.config.Enabled != row.Enabled {
		// Two old writers may have disagreed. A stored false is always retained;
		// a true/false conflict resolves to the conservative disabled decision.
		falseValue := false
		a.config.Enabled = &falseValue
	} else {
		value := row.Enabled
		a.config.Enabled = &value
	}
	if err := mergeConfigPayload(&a.config.Payload, row.Config); err != nil {
		return fmt.Errorf("%w: plugin %s config: %w", ErrLegacyMigrationConflict, def.ID, err)
	}
	return nil
}

func (a *ConfigAccumulator) addManifestOverride(def Definition, row LegacyManifestOverride, creatingDefinition bool) error {
	if !a.hasConfig {
		a.config = Config{PluginID: def.ID, Namespace: def.Namespace, Scope: ScopeSystem, Revision: 1}
		a.hasConfig = true
	}
	if row.Enabled != nil {
		if a.config.Enabled != nil && *a.config.Enabled != *row.Enabled {
			falseValue := false
			a.config.Enabled = &falseValue
		} else {
			a.config.Enabled = cloneBool(row.Enabled)
		}
	}
	if row.Config != "" {
		if !creatingDefinition {
			if err := rejectManifestIdentityOverride(def, row.Config); err != nil {
				return err
			}
		}
		payload, err := normalizeManifestConfig(row.Config)
		if err != nil {
			return fmt.Errorf("manifest %s: %w", def.ID, err)
		}
		if err := mergeConfigPayload(&a.config.Payload, payload); err != nil {
			return fmt.Errorf("%w: manifest %s config: %w", ErrLegacyMigrationConflict, def.ID, err)
		}
	}
	if row.SessionEnvVaultKey != "" {
		refs := map[string]any{"name": row.SessionEnvVaultKey, "scope": "system"}
		encoded, err := json.Marshal(refs)
		if err != nil {
			return err
		}
		a.config.CredentialRefs = json.RawMessage(`{"session_env":` + string(encoded) + `}`)
	}
	return nil
}

func mergeConfigPayload(dst *json.RawMessage, raw json.RawMessage) error {
	if len(raw) == 0 || emptyJSONObject(raw) {
		return nil
	}
	var incoming map[string]json.RawMessage
	if err := json.Unmarshal(raw, &incoming); err != nil || incoming == nil {
		return fmt.Errorf("payload must be an object: %w", err)
	}
	var current map[string]json.RawMessage
	if len(*dst) != 0 {
		if err := json.Unmarshal(*dst, &current); err != nil || current == nil {
			return fmt.Errorf("existing payload must be an object: %w", err)
		}
	} else {
		current = make(map[string]json.RawMessage)
	}
	for key, value := range incoming {
		if old, ok := current[key]; ok && !bytes.Equal(bytes.TrimSpace(old), bytes.TrimSpace(value)) {
			return fmt.Errorf("field %q has conflicting values", key)
		}
		current[key] = bytes.Clone(value)
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return err
	}
	*dst = encoded
	return nil
}

func normalizeManifestConfig(raw string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil || object == nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	// A sparse marker is a storage detail, not backend config. A legacy full
	// snapshot owns every editable field, including omitted fields as null.
	if marker, ok := object["$sparse"]; ok && string(marker) == "true" {
		delete(object, "$sparse")
	} else {
		for _, field := range []string{"name", "display_name", "description", "category", "prompt", "binaries", "skills", "session_env", "oauth_provider"} {
			if _, ok := object[field]; !ok {
				object[field] = json.RawMessage("null")
			}
		}
	}
	for _, field := range []string{"id", "kind", "enabled", "essential", "builtin", "overridden_fields", "name", "display_name"} {
		delete(object, field)
	}
	// Session environment values are literal material in the old snapshot. The
	// vault-key column is a safe locator, but a literal cannot be imported into
	// the target without proving its secret boundary, so fail and preserve the
	// source row for an explicit operator migration.
	if value, ok := object["session_env"]; ok {
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(value, &entries); err != nil {
			return nil, fmt.Errorf("%w: session_env is not a typed list: %w", ErrLegacyMigrationConflict, err)
		}
		for _, entry := range entries {
			if literal, ok := entry["value"]; ok && !bytes.Equal(bytes.TrimSpace(literal), []byte("null")) {
				return nil, fmt.Errorf("%w: session_env literal value requires explicit vault migration", ErrLegacyMigrationConflict)
			}
		}
	}
	return json.Marshal(object)
}

func rejectManifestIdentityOverride(def Definition, raw string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil || object == nil {
		return fmt.Errorf("%w: manifest %s identity JSON: %w", ErrLegacyMigrationConflict, def.ID, err)
	}
	for field, expected := range map[string]string{"name": def.Namespace, "display_name": def.DisplayName} {
		value, ok := object[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		actual := jsonString(value)
		if actual != expected {
			return fmt.Errorf("%w: manifest %s owns %s=%q; identity changes need explicit mapping", ErrLegacyMigrationConflict, def.ID, field, actual)
		}
	}
	return nil
}

func customDefinitionFromManifest(id, raw string) (Definition, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil || object == nil {
		return Definition{}, fmt.Errorf("%w: manifest %s JSON: %w", ErrLegacyMigrationConflict, id, err)
	}
	name := jsonString(object["name"])
	if name == "" {
		name = id
	}
	defSpec, err := normalizeManifestConfig(raw)
	if err != nil {
		return Definition{}, fmt.Errorf("manifest %s: %w", id, err)
	}
	displayName := jsonString(object["display_name"])
	if displayName == "" {
		displayName = name
	}
	kind := jsonString(object["kind"])
	if kind == "go" {
		return Definition{}, fmt.Errorf("%w: custom Go implementation %s", ErrLegacyMigrationConflict, id)
	}
	return Definition{
		ID: id, Namespace: sanitizeLegacyIdent(name, "plugin"), DisplayName: displayName,
		Backend: BackendCLI, Source: SourceCustom, ImplementationKey: "cli",
		Spec: defSpec, DefaultEnabled: false, Revision: 1,
	}, nil
}

func jsonString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func normalizeMCP(row LegacyMCPRegistration) (Definition, Config, error) {
	parsedID, err := uuid.Parse(row.ID)
	if err != nil {
		return Definition{}, Config{}, fmt.Errorf("%w: MCP id %q: %w", ErrLegacyMigrationConflict, row.ID, err)
	}
	if !validLegacyScope(row.Scope) || !legacyOwnerMatches(row.Scope, row.UserID, row.AgentID) {
		return Definition{}, Config{}, fmt.Errorf("%w: MCP %s has invalid owner tuple", ErrLegacyMigrationConflict, row.ID)
	}
	if row.Name == "" || row.URL == "" || (row.Transport != legacyMCPTransportSSE && row.Transport != legacyMCPTransportHTTP) {
		return Definition{}, Config{}, fmt.Errorf("%w: MCP %s has invalid name, URL, or transport", ErrLegacyMigrationConflict, row.ID)
	}
	if !validLegacyMCPAuth(row.AuthType) || (row.CredentialMode != "" && !validLegacyMCPCredentialMode(row.CredentialMode)) {
		return Definition{}, Config{}, fmt.Errorf("%w: MCP %s has invalid auth or credential mode", ErrLegacyMigrationConflict, row.ID)
	}
	namespace := sanitizeLegacyIdent(row.Name, "mcp")
	if _, err := normalizeMCPTools(namespace, row.Tools); err != nil {
		return Definition{}, Config{}, fmt.Errorf("MCP %s tools: %w", row.ID, err)
	}
	metadata, err := normalizeMCPMetadata(row.Metadata)
	if err != nil {
		return Definition{}, Config{}, fmt.Errorf("MCP %s metadata: %w", row.ID, err)
	}
	payload := map[string]any{
		"url": row.URL, "transport": row.Transport, "auth_type": row.AuthType,
		"credential_mode": effectiveCredentialMode(row.CredentialMode), "metadata": metadata,
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return Definition{}, Config{}, err
	}
	refs := mcpCredentialRefs(row, parsedID)
	creator := ""
	if row.Scope == string(ScopeUser) || row.Scope == string(ScopeUserAgent) {
		creator = row.UserID
	}
	def := Definition{
		ID: "custom/" + row.ID, Namespace: namespace, DisplayName: row.Name,
		Backend: BackendMCP, Source: SourceCustom, ImplementationKey: "mcp", Spec: json.RawMessage(`{}`),
		DefaultEnabled: false, Revision: 1, CreatorUserID: creator,
	}
	config := Config{
		ID: row.ID, PluginID: def.ID, Namespace: namespace, Scope: Scope(row.Scope), UserID: row.UserID, AgentID: row.AgentID,
		Enabled: cloneBool(&row.Enabled), Payload: encodedPayload, CredentialRefs: refs, Revision: 1,
	}
	return def, config, nil
}

func normalizeMCPTools(namespace string, raw json.RawMessage) ([]map[string]string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, err
	}
	seen := make(map[string]string, len(tools))
	result := make([]map[string]string, 0, len(tools))
	for _, tool := range tools {
		local := sanitizeLegacyIdent(tool.Name, "tool")
		if prior, ok := seen[local]; ok {
			return nil, fmt.Errorf("local tool names %q and %q collide as %q", prior, tool.Name, local)
		}
		seen[local] = tool.Name
		name, err := ExportedToolName(namespace, local)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]string{"remote_name": tool.Name, "local_name": local, "exported_name": name})
	}
	return result, nil
}

func mcpCredentialRefs(row LegacyMCPRegistration, id uuid.UUID) json.RawMessage {
	refs := make(map[string]any)
	if row.CredentialRef != "" {
		refs["bearer"] = map[string]any{"name": row.CredentialRef, "scope": row.Scope, "user_id": row.UserID, "agent_id": row.AgentID}
	}
	if row.AuthType == legacyMCPAuthOAuth {
		bundle := "MCP_OAUTH_" + strings.ToUpper(strings.ReplaceAll(id.String(), "-", "_"))
		bundleRef := map[string]any{"name": bundle, "mode": effectiveCredentialMode(row.CredentialMode)}
		if effectiveCredentialMode(row.CredentialMode) == legacyMCPCredPerUser {
			// A registration row does not identify every per-user grant. Never
			// claim that its registration tuple owns those user grants.
			bundleRef["owner"] = "per_user"
		} else {
			bundleRef["scope"], bundleRef["user_id"], bundleRef["agent_id"] = row.Scope, row.UserID, row.AgentID
		}
		refs["oauth_bundle"] = bundleRef
		if clientRef := metadataString(row.Metadata, "oauth.client_secret_ref", "client_secret_ref"); clientRef != "" {
			refs["oauth_client_secret"] = map[string]any{"name": clientRef, "scope": row.Scope, "user_id": row.UserID, "agent_id": row.AgentID}
		}
	}
	encoded, _ := json.Marshal(refs)
	return encoded
}

func metadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		parts := strings.Split(key, ".")
		var current any = metadata
		for _, part := range parts {
			object, ok := current.(map[string]any)
			if !ok {
				current = nil
				break
			}
			current = object[part]
		}
		if value, ok := current.(string); ok {
			return value
		}
	}
	return ""
}

func normalizeMCPMetadata(metadata map[string]any) (map[string]any, error) {
	if metadata == nil {
		return map[string]any{}, nil
	}
	result := make(map[string]any, len(metadata))
	for key, value := range metadata {
		switch key {
		case "call_timeout_seconds":
			if _, ok := value.(float64); !ok {
				return nil, fmt.Errorf("%w: metadata.%s must be a number", ErrLegacyMigrationConflict, key)
			}
			result[key] = value
		case "oauth":
			nested, err := normalizeMCPMetadataOAuth(value)
			if err != nil {
				return nil, err
			}
			result[key] = nested
		case "registry":
			nested, err := normalizeMCPMetadataRegistry(value)
			if err != nil {
				return nil, err
			}
			result[key] = nested
		default:
			return nil, fmt.Errorf("%w: unsupported MCP metadata field %q", ErrLegacyMigrationConflict, key)
		}
	}
	return result, nil
}

func normalizeMCPMetadataOAuth(value any) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: metadata.oauth must be an object", ErrLegacyMigrationConflict)
	}
	result := make(map[string]any, len(object))
	for key, entry := range object {
		if key != "client_id" && key != "client_secret_ref" {
			return nil, fmt.Errorf("%w: unsupported MCP metadata.oauth field %q", ErrLegacyMigrationConflict, key)
		}
		if _, ok := entry.(string); !ok {
			return nil, fmt.Errorf("%w: metadata.oauth.%s must be a string", ErrLegacyMigrationConflict, key)
		}
		if key == "client_id" {
			result[key] = entry
		}
		// client_secret_ref is carried exclusively by CredentialRefs.
	}
	return result, nil
}

func normalizeMCPMetadataRegistry(value any) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: metadata.registry must be an object", ErrLegacyMigrationConflict)
	}
	result := make(map[string]any, len(object))
	for key, entry := range object {
		switch key {
		case "source", "id", "version", "installed_at":
			if _, ok := entry.(string); !ok {
				return nil, fmt.Errorf("%w: metadata.registry.%s must be a string", ErrLegacyMigrationConflict, key)
			}
		default:
			return nil, fmt.Errorf("%w: unsupported MCP metadata.registry field %q", ErrLegacyMigrationConflict, key)
		}
		result[key] = entry
	}
	return result, nil
}

// ConvertLegacyToolOverride resolves the old mcp__server__tool key against the
// exact legacy registration owner. Core overrides are returned as zero-value
// migrations and remain in the dormant legacy table until the target schema
// can represent both core and plugin identities.
func ConvertLegacyToolOverride(override LegacyToolOverride, registrations []LegacyMCPRegistration) (ToolOverrideMigration, error) {
	if !validLegacyScope(override.Scope) || !legacyOwnerMatches(override.Scope, override.UserID, override.AgentID) {
		return ToolOverrideMigration{}, ErrLegacyMigrationConflict
	}
	// This finite migration table comes from the old generated catalog. It is
	// not a runtime prefix authorization rule.
	for _, family := range []struct{ namespace, pluginID, locals string }{
		{"email", "system/email", "account_list message_list message_read message_send"},
		{"recally", "system/recally", "article_get article_list article_save digest_get digest_save entry_add entry_list entry_update feed_add feed_list feed_poll feed_remove"},
		{"scheduler", "system/scheduler", "job_create job_delete job_get job_list job_pause job_resume job_update"},
	} {
		for local := range strings.FieldsSeq(family.locals) {
			if override.ToolName != family.namespace+"_"+local {
				continue
			}
			return ToolOverrideMigration{
				LegacyID: override.ID, OldName: override.ToolName,
				NewName: family.namespace + "__" + local, PluginID: family.pluginID, Namespace: family.namespace, LocalTool: local,
				Scope: Scope(override.Scope), UserID: override.UserID, AgentID: override.AgentID, Enabled: override.Enabled,
			}, nil
		}
	}

	server, remote, ok := splitLegacyMCPToolName(override.ToolName)
	if !ok {
		if strings.HasPrefix(override.ToolName, "mcp__") {
			return ToolOverrideMigration{}, fmt.Errorf("%w: malformed MCP tool override %q", ErrLegacyMigrationConflict, override.ToolName)
		}
		return ToolOverrideMigration{}, nil
	}
	var matches []LegacyMCPRegistration
	for _, registration := range registrations {
		if !validLegacyScope(registration.Scope) || !legacyOwnerMatches(registration.Scope, registration.UserID, registration.AgentID) {
			return ToolOverrideMigration{}, ErrLegacyMigrationConflict
		}
		// A policy can constrain a broader shared registration. Candidates are
		// scopes that can intersect in an execution context, not identical tuples.
		if override.UserID != "" && registration.UserID != "" && override.UserID != registration.UserID {
			continue
		}
		if override.AgentID != "" && registration.AgentID != "" && override.AgentID != registration.AgentID {
			continue
		}
		if sanitizeLegacyIdent(registration.Name, "mcp") != server {
			continue
		}
		var tools []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(registration.Tools, &tools); err != nil {
			return ToolOverrideMigration{}, fmt.Errorf("%w: MCP %s tools catalog: %w", ErrLegacyMigrationConflict, registration.ID, err)
		}
		for _, tool := range tools {
			if sanitizeLegacyIdent(tool.Name, "tool") == remote {
				matches = append(matches, registration)
			}
		}
	}
	if len(matches) != 1 {
		return ToolOverrideMigration{}, fmt.Errorf("%w: %s matches %d MCP registrations", ErrLegacyMigrationConflict, override.ToolName, len(matches))
	}
	registration := matches[0]
	local := sanitizeLegacyIdent(remote, "tool")
	newName, err := ExportedToolName(sanitizeLegacyIdent(registration.Name, "mcp"), local)
	if err != nil {
		return ToolOverrideMigration{}, err
	}
	return ToolOverrideMigration{
		LegacyID: override.ID, OldName: override.ToolName, NewName: newName,
		PluginID: "custom/" + registration.ID, ConfigID: registration.ID, Namespace: sanitizeLegacyIdent(registration.Name, "mcp"), LocalTool: local,
		Scope: Scope(override.Scope), UserID: override.UserID, AgentID: override.AgentID, Enabled: override.Enabled,
	}, nil
}

func validLegacyScope(scope string) bool {
	switch Scope(scope) {
	case ScopeSystem, ScopeSystemAgent, ScopeUser, ScopeUserAgent:
		return true
	default:
		return false
	}
}

func legacyOwnerMatches(scope, userID, agentID string) bool {
	switch Scope(scope) {
	case ScopeSystem:
		return userID == "" && agentID == ""
	case ScopeSystemAgent:
		return userID == "" && agentID != ""
	case ScopeUser:
		return userID != "" && agentID == ""
	case ScopeUserAgent:
		return userID != "" && agentID != ""
	default:
		return false
	}
}

func effectiveCredentialMode(mode string) string {
	if mode == "" {
		return legacyMCPCredShared
	}
	return mode
}

func sanitizeLegacyIdent(value, fallback string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		result = fallback
	}
	return result
}

func validLegacyMCPAuth(auth string) bool {
	return auth == legacyMCPAuthNone || auth == legacyMCPAuthBearer || auth == legacyMCPAuthOAuth
}

func validLegacyMCPCredentialMode(mode string) bool {
	return mode == legacyMCPCredShared || mode == legacyMCPCredPerUser
}

func splitLegacyMCPToolName(name string) (server, tool string, ok bool) {
	const prefix = "mcp__"
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, prefix)
	separator := strings.Index(rest, "__")
	if separator <= 0 || separator+2 >= len(rest) {
		return "", "", false
	}
	return rest[:separator], rest[separator+2:], true
}
