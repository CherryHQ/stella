package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/plugin"
)

var errLegacyFileCredentialMigration = errors.New("mcp: legacy file credential migration conflict")

// MigrateLegacyFileCredentials moves credentials for materialized MCP files.
// It must run inside the cutover transaction. Vault is bound to that same
// transaction, so the grant fence and its secret cannot commit independently.
func (s *Service) MigrateLegacyFileCredentials(ctx context.Context, tx pgx.Tx, resources *plugin.ResourceStore, mappings []plugin.MCPMigrationEvidence) error {
	if len(mappings) == 0 {
		return nil
	}
	if tx == nil || resources == nil {
		return fmt.Errorf("%w: transaction and resource store are required", errLegacyFileCredentialMigration)
	}
	ordered := slices.Clone(mappings)
	slices.SortFunc(ordered, func(a, b plugin.MCPMigrationEvidence) int {
		if c := strings.Compare(a.OldConfigID, b.OldConfigID); c != 0 {
			return c
		}
		if c := strings.Compare(a.OldServerKey, b.OldServerKey); c != 0 {
			return c
		}
		return strings.Compare(a.NewResource.ID(), b.NewResource.ID())
	})

	prepared := make([]legacyFileCredentialMigration, 0, len(ordered))
	seenTargets := make(map[string]struct{}, len(ordered))
	for _, evidence := range ordered {
		if evidence.OldConfigID == "" || evidence.OldServerKey == "" || evidence.NewResource.ID() == "" {
			return fmt.Errorf("%w: incomplete MCP mapping", errLegacyFileCredentialMigration)
		}
		oldReg, err := lockLegacyRegistration(ctx, tx, evidence.OldConfigID, evidence.OldServerKey)
		if err != nil {
			return err
		}
		fileReg, err := fileMigrationRegistration(ctx, resources, evidence.NewResource, evidence.OldServerKey)
		if err != nil {
			return err
		}
		if err := validateFileMigrationIdentity(oldReg, fileReg); err != nil {
			return err
		}
		key := fileReg.ID
		if _, exists := seenTargets[key]; exists {
			return fmt.Errorf("%w: duplicate target %q", errLegacyFileCredentialMigration, key)
		}
		seenTargets[key] = struct{}{}
		rows, err := listLegacyCredentialOwners(ctx, tx, oldReg)
		if err != nil {
			return err
		}
		prepared = append(prepared, legacyFileCredentialMigration{old: oldReg, file: fileReg, rows: rows})
	}

	// Parent rows are locked above, in mapping order. File grants have no row
	// identity, therefore take all advisory locks in one sorted order next.
	locks := make(map[string]struct{})
	for _, migration := range prepared {
		for _, owner := range migration.targetOwners() {
			locks[fileAdvisoryKey(migration.file, owner)] = struct{}{}
		}
	}
	lockKeys := slices.Sorted(maps.Keys(locks))
	for _, key := range lockKeys {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
			return fmt.Errorf("%w: lock target grant: %w", errLegacyFileCredentialMigration, err)
		}
	}

	vault := s.boundMigrationVault(tx)
	if vault == nil && hasLegacyCredential(prepared) {
		return fmt.Errorf("%w: Vault is unavailable while source credentials exist", errLegacyFileCredentialMigration)
	}
	for _, migration := range prepared {
		if err := expireLegacyOAuthFlows(ctx, tx, migration.old.ID); err != nil {
			return err
		}
		if vault == nil {
			continue
		}
		if err := migrateOneLegacyFileCredential(ctx, vault, migration); err != nil {
			return err
		}
	}
	return nil
}

type legacyFileCredentialMigration struct {
	old  Registration
	file Registration
	rows []legacyVaultOwner
}

type legacyVaultOwner struct {
	owner CredentialOwner
	name  string
}

func (m legacyFileCredentialMigration) targetOwners() []CredentialOwner {
	owners := make(map[string]CredentialOwner)
	switch {
	case m.file.CredentialMode != CredentialModePerUser:
		owner := CredentialOwner{Scope: m.file.Scope, UserID: m.file.UserID, AgentID: m.file.AgentID}
		owners[ownerKey(owner)] = owner
	case m.old.AuthType == AuthTypeBearer:
		// Private legacy bearer rows are declaration-owned, but the file
		// projection converts them to a per-user grant.
		if m.old.UserID != "" {
			owner := CredentialOwner{Scope: ScopeUser, UserID: m.old.UserID}
			owners[ownerKey(owner)] = owner
		}
	default:
		for _, row := range m.rows {
			if row.name == oauthBundleName(m.old.ID) && row.owner.Scope == ScopeUser {
				owner := CredentialOwner{Scope: ScopeUser, UserID: row.owner.UserID}
				owners[ownerKey(owner)] = owner
			}
		}
	}
	if m.file.AuthType == AuthTypeOAuth && m.file.OAuthClientSecretRef != "" {
		owner := fileClientOwner(m.file)
		owners[ownerKey(owner)] = owner
	}
	result := make([]CredentialOwner, 0, len(owners))
	for _, owner := range owners {
		result = append(result, owner)
	}
	slices.SortFunc(result, func(a, b CredentialOwner) int { return strings.Compare(ownerKey(a), ownerKey(b)) })
	return result
}

func ownerKey(owner CredentialOwner) string {
	return owner.Scope + "\x00" + owner.UserID + "\x00" + owner.AgentID
}

func (s *Service) boundMigrationVault(tx pgx.Tx) Vault {
	if s == nil || s.vault == nil || s.bindVault == nil {
		return nil
	}
	return s.bindVault(tx)
}

func hasLegacyCredential(migrations []legacyFileCredentialMigration) bool {
	for _, migration := range migrations {
		if len(migration.rows) != 0 {
			return true
		}
	}
	return false
}

func lockLegacyRegistration(ctx context.Context, tx pgx.Tx, configID, serverKey string) (Registration, error) {
	var (
		childID, cfgID, pluginID, scope, displayName, source string
		spec, payload, refs                                  []byte
		revision, definitionRevision                         int64
		enabled, defaultEnabled                              pgtype.Bool
		cfgUserID, cfgAgentID, cfgCreatorID                  pgtype.Text
	)
	// Lock the parent first. Credential writers use the same order.
	err := tx.QueryRow(ctx, `
		SELECT c.id, c.plugin_id, c.scope, c.user_id::text, c.agent_id,
		       c.enabled, c.config, c.credential_refs, c.revision,
		       d.display_name, d.source, d.spec, d.default_enabled, d.revision,
		       d.creator_user_id::text
		FROM plugin_config c
		JOIN plugin_definition d ON d.id = c.plugin_id
		WHERE c.id = $1::uuid
		FOR UPDATE OF c`, configID).Scan(&cfgID, &pluginID, &scope, &cfgUserID, &cfgAgentID,
		&enabled, &payload, &refs, &revision, &displayName, &source, &spec, &defaultEnabled, &definitionRevision, &cfgCreatorID)
	if err != nil {
		return Registration{}, fmt.Errorf("%w: lock legacy config %s: %w", errLegacyFileCredentialMigration, configID, err)
	}
	var lockedServerKey string
	if err := tx.QueryRow(ctx, `
		SELECT id, server_key FROM plugin_config_mcp_server
		WHERE config_id = $1::uuid AND server_key = $2
		FOR UPDATE`, configID, serverKey).Scan(&childID, &lockedServerKey); err != nil {
		return Registration{}, fmt.Errorf("%w: lock legacy MCP child %s/%s: %w", errLegacyFileCredentialMigration, configID, serverKey, err)
	}
	var child plugin.MCPServerChild
	child.ID, child.ParentConfigID, child.ServerKey = childID, cfgID, lockedServerKey
	cfg := plugin.Config{ID: cfgID, PluginID: pluginID, Scope: plugin.Scope(scope), UserID: migrationTextValue(cfgUserID), AgentID: migrationTextValue(cfgAgentID), Enabled: nullableBoolValue(enabled), Payload: payload, CredentialRefs: refs, Revision: revision, MCPServers: []plugin.MCPServerChild{child}}
	def := plugin.Definition{ID: pluginID, DisplayName: displayName, Source: plugin.Source(source), Spec: spec, DefaultEnabled: defaultEnabled.Bool, Revision: definitionRevision, CreatorUserID: migrationTextValue(cfgCreatorID)}
	if err := def.Validate(); err != nil {
		return Registration{}, fmt.Errorf("%w: legacy definition: %w", errLegacyFileCredentialMigration, err)
	}
	effectivePayload, err := plugin.MergeDefinitionConfig(def.Spec, cfg.Payload)
	if err != nil {
		return Registration{}, fmt.Errorf("%w: merge legacy MCP config: %w", errLegacyFileCredentialMigration, err)
	}
	authority, err := migrationAuthority(cfg.Scope, cfg.UserID, cfg.AgentID)
	if err != nil {
		return Registration{}, err
	}
	return RegistrationFromPluginChild(def, cfg, plugin.Effective{PluginID: pluginID, ConfigID: cfgID, SourceScope: cfg.Scope, IsEffectivelyEnabled: enabled.Bool, Payload: effectivePayload}, child, PluginMCPObservation{}, authority)
}

func migrationTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullableBoolValue(value pgtype.Bool) *bool {
	if !value.Valid {
		return nil
	}
	result := value.Bool
	return &result
}

func migrationAuthority(scope plugin.Scope, userID, agentID string) (authz.Authority, error) {
	// System resources are migrated by named maintenance work. System-agent
	// resources use the same shared, deployment-owned contract: there is no
	// durable human owner to turn into a user capability.
	if scope == plugin.ScopeSystem || scope == plugin.ScopeSystemAgent {
		if userID != "" || (scope == plugin.ScopeSystem && agentID != "") || (scope == plugin.ScopeSystemAgent && agentID == "") {
			return authz.Authority{}, fmt.Errorf("%w: invalid %s migration owner", errLegacyFileCredentialMigration, scope)
		}
		return agentaccess.SystemAgentAuthority("legacy-file-credential-migration")
	}
	if scope != plugin.ScopeUser && scope != plugin.ScopeUserAgent {
		return authz.Authority{}, fmt.Errorf("%w: invalid migration scope %q", errLegacyFileCredentialMigration, scope)
	}
	if userID == "" || (scope == plugin.ScopeUser && agentID != "") || (scope == plugin.ScopeUserAgent && agentID == "") {
		return authz.Authority{}, fmt.Errorf("%w: invalid %s migration owner", errLegacyFileCredentialMigration, scope)
	}
	identity := authz.Identity{UserID: userID, AgentID: agentID, AgentScoped: scope == plugin.ScopeUserAgent}
	return identity.ToAuthority()
}

func fileMigrationRegistration(ctx context.Context, resources *plugin.ResourceStore, key plugin.ResourceKey, oldServerKey string) (Registration, error) {
	resource, err := resources.Get(ctx, key)
	if err != nil {
		return Registration{}, fmt.Errorf("%w: read target resource %s: %w", errLegacyFileCredentialMigration, key.ID(), err)
	}
	// A disabled resource still owns its authored credential namespace. Clear
	// policy flags only in this local projection; files and settings are not changed.
	resource.Disabled, resource.Forbidden = false, false
	serverKey := ""
	if key.Kind == plugin.ResourcePlugin {
		serverKey = oldServerKey
	}
	reg, err := registrationFromFileResource(resource, serverKey)
	if err != nil {
		return Registration{}, fmt.Errorf("%w: target declaration: %w", errLegacyFileCredentialMigration, err)
	}
	return reg, nil
}

func validateFileMigrationIdentity(old, file Registration) error {
	if old.AuthType != file.AuthType || old.URL != file.URL || old.Transport != file.Transport ||
		!maps.Equal(old.Headers, file.Headers) || old.OAuthClientID != file.OAuthClientID {
		return fmt.Errorf("%w: source and target MCP identity differ", errLegacyFileCredentialMigration)
	}
	if old.Scope != string(file.FileKey.Scope) || old.UserID != file.FileKey.UserID || old.AgentID != file.FileKey.AgentID {
		return fmt.Errorf("%w: source and target MCP owner differs", errLegacyFileCredentialMigration)
	}
	allowedModeConversion := old.AuthType == AuthTypeBearer && old.CredentialMode == CredentialModeShared &&
		(old.Scope == ScopeUser || old.Scope == ScopeUserAgent) && file.CredentialMode == CredentialModePerUser
	if old.CredentialMode != file.CredentialMode && !allowedModeConversion {
		return fmt.Errorf("%w: source and target credential mode differs", errLegacyFileCredentialMigration)
	}
	// File declarations own their locator namespace. A materializer may choose
	// a fresh target name, so only presence is part of the identity check; the
	// old registration's locator remains the source Vault lookup below.
	if old.AuthType == AuthTypeBearer && file.CredentialRef == "" {
		return fmt.Errorf("%w: target bearer locator is empty", errLegacyFileCredentialMigration)
	}
	if old.AuthType == AuthTypeOAuth {
		if file.CredentialRef == "" {
			return fmt.Errorf("%w: target OAuth bundle locator is empty", errLegacyFileCredentialMigration)
		}
		if old.OAuthClientSecretRef != "" && file.OAuthClientSecretRef == "" {
			return fmt.Errorf("%w: target OAuth client-secret locator is empty", errLegacyFileCredentialMigration)
		}
	}
	return nil
}

func listLegacyCredentialOwners(ctx context.Context, tx pgx.Tx, reg Registration) ([]legacyVaultOwner, error) {
	names := make([]string, 0, 3)
	switch reg.AuthType {
	case AuthTypeBearer:
		names = append(names, reg.CredentialRef)
	case AuthTypeOAuth:
		names = append(names, oauthBundleName(reg.ID))
		if reg.OAuthClientSecretRef != "" {
			names = append(names, oauthClientSecretName(reg.ID))
		}
	default:
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT scope, coalesce(user_id::text, ''), coalesce(agent_id, ''), name
		FROM vault_entry WHERE name = ANY($1::text[])
		ORDER BY scope, coalesce(user_id::text, ''), coalesce(agent_id, ''), name`, names)
	if err != nil {
		return nil, fmt.Errorf("%w: list source Vault owners: %w", errLegacyFileCredentialMigration, err)
	}
	defer rows.Close()
	var result []legacyVaultOwner
	for rows.Next() {
		var scope, userID, agentID, name string
		if err := rows.Scan(&scope, &userID, &agentID, &name); err != nil {
			return nil, fmt.Errorf("%w: scan source Vault owner: %w", errLegacyFileCredentialMigration, err)
		}
		owner, err := validMigrationOwner(scope, userID, agentID)
		if err != nil {
			return nil, err
		}
		if !legacyOwnerAllowed(reg, owner, name) {
			return nil, fmt.Errorf("%w: source Vault owner %s/%s/%s is not valid for %s", errLegacyFileCredentialMigration, scope, userID, agentID, name)
		}
		result = append(result, legacyVaultOwner{owner: owner, name: name})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read source Vault owners: %w", errLegacyFileCredentialMigration, err)
	}
	return result, nil
}

func validMigrationOwner(scope, userID, agentID string) (CredentialOwner, error) {
	owner := CredentialOwner{Scope: scope, UserID: userID, AgentID: agentID}
	switch scope {
	case ScopeSystem:
		if userID != "" || agentID != "" {
			return CredentialOwner{}, fmt.Errorf("%w: invalid system Vault owner", errLegacyFileCredentialMigration)
		}
	case ScopeSystemAgent:
		if userID != "" || agentID == "" {
			return CredentialOwner{}, fmt.Errorf("%w: invalid system-agent Vault owner", errLegacyFileCredentialMigration)
		}
	case ScopeUser:
		if userID == "" || agentID != "" {
			return CredentialOwner{}, fmt.Errorf("%w: invalid user Vault owner", errLegacyFileCredentialMigration)
		}
	case ScopeUserAgent:
		if userID == "" || agentID == "" {
			return CredentialOwner{}, fmt.Errorf("%w: invalid user-agent Vault owner", errLegacyFileCredentialMigration)
		}
	default:
		return CredentialOwner{}, fmt.Errorf("%w: invalid Vault scope %q", errLegacyFileCredentialMigration, scope)
	}
	return owner, nil
}

func legacyOwnerAllowed(reg Registration, owner CredentialOwner, name string) bool {
	configOwner := CredentialOwner{Scope: reg.Scope, UserID: reg.UserID, AgentID: reg.AgentID}
	if name == oauthClientSecretName(reg.ID) || reg.AuthType == AuthTypeBearer {
		return owner == configOwner
	}
	if reg.CredentialMode == CredentialModeShared {
		return owner == configOwner
	}
	return name == oauthBundleName(reg.ID) && owner.Scope == ScopeUser && owner.UserID != "" && owner.AgentID == ""
}

func migrateOneLegacyFileCredential(ctx context.Context, vault Vault, migration legacyFileCredentialMigration) error {
	old := migration.old
	file := migration.file
	if old.AuthType == AuthTypeBearer {
		owner := CredentialOwner{Scope: file.Scope, UserID: file.UserID, AgentID: file.AgentID}
		if file.CredentialMode == CredentialModePerUser {
			owner = CredentialOwner{Scope: ScopeUser, UserID: old.UserID}
		}
		if _, raw, err := readFileGrant(ctx, vault, owner, file); err != nil {
			return err
		} else if raw != "" {
			return nil
		}
		var token string
		for _, row := range migration.rows {
			if row.name != old.CredentialRef {
				continue
			}
			var err error
			token, err = migrationVaultGet(ctx, vault, row.owner, row.name)
			if err != nil {
				return err
			}
		}
		if token == "" {
			return nil
		}
		return migrateBearer(ctx, vault, file, owner, token)
	}
	if old.AuthType != AuthTypeOAuth {
		return nil
	}
	secret := ""
	for _, row := range migration.rows {
		if row.name != oauthClientSecretName(old.ID) {
			continue
		}
		value, err := migrationVaultGet(ctx, vault, row.owner, row.name)
		if err != nil {
			return err
		}
		secret = value
		break
	}
	if file.OAuthClientSecretRef != "" && secret != "" {
		owner := CredentialOwner{Scope: file.Scope, UserID: file.UserID, AgentID: file.AgentID}
		if err := preserveClientSecret(ctx, vault, file, owner, secret); err != nil {
			return err
		}
	}
	for _, row := range migration.rows {
		if row.name != oauthBundleName(old.ID) {
			continue
		}
		targetOwner := CredentialOwner{Scope: file.Scope, UserID: file.UserID, AgentID: file.AgentID}
		if file.CredentialMode == CredentialModePerUser {
			targetOwner = CredentialOwner{Scope: ScopeUser, UserID: row.owner.UserID}
		}
		if _, grantRaw, err := readFileGrant(ctx, vault, targetOwner, file); err != nil {
			return err
		} else if grantRaw != "" {
			continue
		}
		raw, err := migrationVaultGet(ctx, vault, row.owner, row.name)
		if err != nil {
			return err
		}
		if raw == "" {
			continue
		}
		var bundle OAuthBundle
		if err := json.Unmarshal([]byte(raw), &bundle); err != nil || bundle.Version < 1 || bundle.ClientID == "" || bundle.AccessToken == "" || bundle.TokenEndpoint == "" {
			return fmt.Errorf("%w: invalid OAuth bundle for %s", errLegacyFileCredentialMigration, old.ID)
		}
		if err := migrateBundle(ctx, vault, file, targetOwner, bundle); err != nil {
			return err
		}
	}
	return nil
}

func migrateBearer(ctx context.Context, vault Vault, reg Registration, owner CredentialOwner, token string) error {
	grant, raw, err := readFileGrant(ctx, vault, owner, reg)
	if err != nil {
		return err
	}
	if raw != "" || grant.Generation != "" || grant.Revoked {
		return nil
	}
	if err := migrationVaultSet(ctx, vault, owner, reg.CredentialRef, token); err != nil {
		return err
	}
	grant.Generation = uuid.Must(uuid.NewV7()).String()
	grant.Revoked = false
	_, err = writeFileGrant(ctx, vault, owner, reg, grant)
	return err
}

func migrateBundle(ctx context.Context, vault Vault, reg Registration, owner CredentialOwner, bundle OAuthBundle) error {
	grant, raw, err := readFileGrant(ctx, vault, owner, reg)
	if err != nil {
		return err
	}
	if raw != "" || grant.Generation != "" || grant.Revoked {
		return nil
	}
	grant.Generation = uuid.Must(uuid.NewV7()).String()
	grant.Revoked = false
	bundle.Generation = grant.Generation
	grant.Bundle = &bundle
	_, err = writeFileGrant(ctx, vault, owner, reg, grant)
	return err
}

func preserveClientSecret(ctx context.Context, vault Vault, reg Registration, owner CredentialOwner, secret string) error {
	current, err := fileVaultGet(ctx, vault, owner, reg.OAuthClientSecretRef)
	if err == nil && current != "" {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return fileVaultSet(ctx, vault, owner, reg.OAuthClientSecretRef, secret)
}

func migrationVaultGet(ctx context.Context, vault Vault, owner CredentialOwner, name string) (string, error) {
	if IsSystemScope(owner.Scope) {
		return vault.GetScoped(ctx, owner.Scope, "", owner.AgentID, name)
	}
	return vault.GetScoped(ctx, owner.Scope, owner.UserID, owner.AgentID, name)
}

func migrationVaultSet(ctx context.Context, vault Vault, owner CredentialOwner, name, value string) error {
	if IsSystemScope(owner.Scope) {
		return vault.SetSystemScoped(ctx, owner.Scope, owner.AgentID, name, value)
	}
	return vault.SetScoped(ctx, owner.Scope, owner.UserID, owner.AgentID, name, value)
}

func expireLegacyOAuthFlows(ctx context.Context, tx pgx.Tx, childID string) error {
	if _, err := tx.Exec(ctx, `UPDATE mcp_oauth_flow SET consumed_at = coalesce(consumed_at, now()), expires_at = least(expires_at, now()) WHERE server_id = $1::uuid AND consumed_at IS NULL`, childID); err != nil {
		return fmt.Errorf("%w: expire OAuth flows for %s: %w", errLegacyFileCredentialMigration, childID, err)
	}
	return nil
}
