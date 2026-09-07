package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var (
	ErrConflict      = errors.New("plugin: revision conflict")
	ErrBuiltinConfig = errors.New("plugin: builtin system config cannot be deleted")
	ErrNotFound      = authz.ErrNotFound
)

// ConfigPatch distinguishes omitted fields from explicit null ownership. The
// persistence port always receives the resulting complete row.
type ConfigPatch struct {
	EnabledSet bool
	Enabled    *bool
	PayloadSet bool
	Payload    json.RawMessage
	// BinaryVersions is a typed write-only edit for the CLI backend. It lets
	// callers change the visible version without sending locator or options back.
	BinaryVersionsSet bool
	BinaryVersions    map[string]string
	CredentialRefsSet bool
	CredentialRefs    json.RawMessage
	ResetFields       []string
}

// PayloadValidator always checks field permissions, types, secrets and locator
// ownership. Enabled=false suppresses only completeness/readiness requirements.
// Config.Payload is already resolved against Definition.Spec by the service;
// backend validators must not merge it as ConfigParameters a second time.
// A new disabled definition is checked with a nil payload to validate its
// immutable resource schema without requiring connection fields.
// CLI system scopes may configure host installation; user scopes are confined
// to sandbox-local versions/options and runtime credentials, never system PATH,
// shared install directories or host hooks. Runtime use must validate again
// after caps lift; storing a disabled configuration is not execution approval.
type PayloadValidator func(context.Context, Definition, Config, []string) error

// BackendPolicy is the single backend boundary for plugin configuration. The
// transition runs after the SQL row mutation and before the enclosing commit.
type BackendPolicy struct {
	Validate   PayloadValidator
	Transition BackendTransition
}

type BackendTransition func(context.Context, pgx.Tx, authz.Authority, MutationKind, Definition, *Config, *Config) error

type MutationKind string

const (
	MutationCreate MutationKind = "create"
	MutationUpdate MutationKind = "update"
	MutationMove   MutationKind = "move"
	MutationReset  MutationKind = "reset"
	MutationDelete MutationKind = "delete"
)

// Service owns persistence; Access is its only caller-facing authorization boundary.
type Service struct {
	db            *pgxpool.Pool
	q             *sqlc.Queries
	agents        *agentaccess.Service
	catalog       *Catalog
	policy        BackendPolicy
	mutationTx    pgx.Tx
	mutationFence MutationFence
	txBound       bool
	contentStore  *ContentStore
}

type ServiceOption func(*Service)

func WithContentStore(store *ContentStore) ServiceOption {
	return func(service *Service) { service.contentStore = store }
}

func NewService(db *pgxpool.Pool, agents *agentaccess.Service, catalog *Catalog, policy BackendPolicy, mutationFence MutationFence, options ...ServiceOption) *Service {
	shipped := NewCatalog()
	if catalog != nil {
		for _, def := range catalog.Definitions() {
			shipped.byID[def.ID] = cloneDefinition(def)
		}
	}
	service := &Service{db: db, q: sqlc.New(db), agents: agents, catalog: shipped, policy: policy, mutationFence: mutationFence}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *Service) Begin(authority authz.Authority) (*Access, error) {
	if s == nil || s.db == nil || !authority.Valid() || authority.Kind() != authz.ActorUser {
		return nil, ErrForbidden
	}
	return &Access{service: s, authority: authority}, nil
}

func (b *Access) GetDefinition(ctx context.Context, id string) (Definition, error) {
	if err := b.ensureActive(); err != nil {
		return Definition{}, err
	}
	def, err := b.service.getDefinition(ctx, id)
	if err != nil {
		return Definition{}, err
	}
	visible, err := b.definitionVisible(ctx, def)
	if err != nil {
		return Definition{}, err
	}
	if !visible {
		return Definition{}, ErrNotFound
	}
	return def, nil
}

func (b *Access) definitionVisible(ctx context.Context, def Definition) (bool, error) {
	if def.Source == SourceBuiltin || b.authority.IsAdmin() {
		return true, nil
	}
	rows, err := b.service.q.ListPluginConfigs(ctx, def.ID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		config := fromSQLConfig(row)
		if config.UserID == "" && len(config.Payload) == 0 {
			continue
		}
		if config.UserID != "" && config.UserID != string(b.authority.UserID()) {
			continue
		}
		if config.AgentID != "" {
			if b.service.agents == nil {
				continue
			}
			if err := b.service.agents.Authorize(ctx, b.authority, config.AgentID, authz.ActionRead); err != nil {
				if errors.Is(err, authz.ErrForbidden) || errors.Is(err, authz.ErrNotFound) {
					continue
				}
				return false, err
			}
		}
		return true, nil
	}
	return false, nil
}

func (b *Access) ListDefinitions(ctx context.Context) ([]Definition, error) {
	if err := b.ensureActive(); err != nil {
		return nil, err
	}
	defs, err := b.service.listDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Definition, 0, len(defs))
	for _, def := range defs {
		if def.Source == SourceBuiltin {
			if _, ok := b.service.catalog.Get(def.ID); !ok {
				continue
			}
		}
		visible, err := b.definitionVisible(ctx, def)
		if err != nil {
			return nil, err
		}
		if visible {
			out = append(out, def)
		}
	}
	return out, nil
}

func (b *Access) ListConfigs(ctx context.Context, pluginID string, scope Scope, agentID string) ([]Config, error) {
	if err := b.ensureActive(); err != nil {
		return nil, err
	}
	def, err := b.GetDefinition(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	userID, ownerAgentID, err := b.owner(ctx, scope, agentID)
	if err != nil {
		return nil, err
	}
	return b.service.listConfigs(ctx, def.ID, scope, userID, ownerAgentID)
}

func (b *Access) GetConfig(ctx context.Context, pluginID, id string) (Config, error) {
	if err := b.ensureActive(); err != nil {
		return Config{}, err
	}
	row, err := b.service.q.GetPluginConfigForOwner(ctx, sqlc.GetPluginConfigForOwnerParams{
		ID: id, PluginID: pluginID, IsAdmin: b.authority.IsAdmin(), ViewerUserID: string(b.authority.UserID()),
	})
	if err != nil {
		return Config{}, mapNotFound(err)
	}
	config := fromSQLConfig(row)
	if err := b.service.loadMCPServerChildren(ctx, &config); err != nil {
		return Config{}, err
	}
	if config.PluginID != pluginID {
		return Config{}, ErrNotFound
	}
	ownerUserID, ownerAgentID, err := b.owner(ctx, config.Scope, config.AgentID)
	if err != nil {
		if errors.Is(err, authz.ErrForbidden) || errors.Is(err, authz.ErrNotFound) {
			return Config{}, ErrNotFound
		}
		return Config{}, err
	}
	if config.UserID != ownerUserID || config.AgentID != ownerAgentID {
		return Config{}, ErrNotFound
	}
	if _, err := b.service.getDefinition(ctx, pluginID); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (b *Access) CreateConfig(ctx context.Context, config Config) (Config, error) {
	if err := b.ensureActive(); err != nil {
		return Config{}, err
	}
	if !b.service.txBound {
		var created Config
		err := b.service.WithMutationTx(ctx, b.authority, func(mutationCtx context.Context, bound *Access, _ pgx.Tx) error {
			var err error
			created, err = bound.CreateConfig(mutationCtx, config)
			return err
		})
		return created, err
	}
	def, err := b.GetDefinition(ctx, config.PluginID)
	if err != nil {
		return Config{}, err
	}
	if err := ensureActiveDefinition(def); err != nil {
		return Config{}, err
	}
	userID, agentID, err := b.owner(ctx, config.Scope, config.AgentID)
	if err != nil {
		return Config{}, err
	}
	config.PluginID = def.ID
	config.UserID, config.AgentID = userID, agentID
	id, err := uuid.NewV7()
	if err != nil {
		return Config{}, err
	}
	config.ID, config.Revision = id.String(), 1
	config.CredentialRefs = nonEmptyJSON(config.CredentialRefs)
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	if err := rejectImmutableSkillPayload(config.Payload); err != nil {
		return Config{}, err
	}
	if err := b.service.validateResolved(ctx, def, config, nil); err != nil {
		return Config{}, err
	}
	created, err := b.service.createConfig(ctx, config)
	if err != nil {
		return Config{}, err
	}
	if err := b.service.transitionConfig(ctx, b.authority, MutationCreate, def, nil, &created); err != nil {
		return Config{}, err
	}
	return created, nil
}

func (b *Access) UpdateConfig(ctx context.Context, pluginID, id string, expectedRevision int64, patch ConfigPatch) (Config, error) {
	if err := b.ensureActive(); err != nil {
		return Config{}, err
	}
	if !b.service.txBound {
		var updated Config
		err := b.service.WithMutationTx(ctx, b.authority, func(mutationCtx context.Context, bound *Access, _ pgx.Tx) error {
			var err error
			updated, err = bound.UpdateConfig(mutationCtx, pluginID, id, expectedRevision, patch)
			return err
		})
		return updated, err
	}
	current, err := b.GetConfig(ctx, pluginID, id)
	if err != nil {
		return Config{}, err
	}
	if expectedRevision < 1 {
		return Config{}, ErrConflict
	}
	def, err := b.GetDefinition(ctx, current.PluginID)
	if err != nil {
		return Config{}, err
	}
	if err := ensureActiveDefinition(def); err != nil {
		return Config{}, err
	}
	if err := rejectImmutableSkillPatch(patch); err != nil {
		return Config{}, err
	}
	if patch.BinaryVersionsSet {
		typedPayload, err := applyCLIWriteOnlyPatch(def, current.Payload, patch)
		if err != nil {
			return Config{}, err
		}
		patch.PayloadSet = true
		patch.Payload = typedPayload
	}
	updated := current
	if patch.EnabledSet {
		updated.Enabled = patch.Enabled
	}
	updated.Payload, err = patchPayload(current.Payload, patch)
	if err != nil {
		return Config{}, err
	}
	if patch.CredentialRefsSet {
		updated.CredentialRefs = patch.CredentialRefs
	}
	if err := updated.Validate(); err != nil {
		return Config{}, err
	}
	closingOnly := patch.EnabledSet && patch.Enabled != nil && !*patch.Enabled &&
		!patch.PayloadSet && !patch.CredentialRefsSet && len(patch.ResetFields) == 0
	// An obsolete payload must never prevent its owner from closing it. Any
	// accompanying field write still goes through the backend safety boundary.
	if !closingOnly {
		if err := b.service.validateResolved(ctx, def, updated, patch.ResetFields); err != nil {
			return Config{}, err
		}
	}
	result, err := b.service.updateConfigCAS(ctx, current.ID, expectedRevision, updated.Enabled, updated.Payload, updated.CredentialRefs)
	if err != nil {
		return Config{}, err
	}
	if err := b.service.transitionConfig(ctx, b.authority, MutationUpdate, def, &current, &result); err != nil {
		return Config{}, err
	}
	return result, nil
}

// MoveConfig changes a config's scope while preserving its config ID. Both the
// source tuple and the target tuple are authorized through the same owner/PEP
// boundary; target user identity is always derived from the bound authority.
// The complete resulting config is validated before one CAS update changes the
// ownership tuple and payload atomically.
func (b *Access) MoveConfig(ctx context.Context, pluginID, id string, expectedRevision int64, targetScope Scope, targetAgentID string, patch ConfigPatch) (Config, error) {
	if err := b.ensureActive(); err != nil {
		return Config{}, err
	}
	if !b.service.txBound {
		var moved Config
		err := b.service.WithMutationTx(ctx, b.authority, func(mutationCtx context.Context, bound *Access, _ pgx.Tx) error {
			var err error
			moved, err = bound.MoveConfig(mutationCtx, pluginID, id, expectedRevision, targetScope, targetAgentID, patch)
			return err
		})
		return moved, err
	}
	if expectedRevision < 1 {
		return Config{}, ErrConflict
	}
	current, err := b.GetConfig(ctx, pluginID, id)
	if err != nil {
		return Config{}, err
	}
	def, err := b.GetDefinition(ctx, current.PluginID)
	if err != nil {
		return Config{}, err
	}
	if err := ensureActiveDefinition(def); err != nil {
		return Config{}, err
	}
	if err := rejectImmutableSkillPatch(patch); err != nil {
		return Config{}, err
	}
	if def.Source == SourceBuiltin && current.Scope == ScopeSystem {
		return Config{}, ErrBuiltinConfig
	}
	// Re-check both tuples at the mutation boundary. GetConfig authorizes the
	// source tuple; owner derives and checks the destination tuple, including
	// AgentPEP access for agent scopes.
	if _, _, err := b.owner(ctx, current.Scope, current.AgentID); err != nil {
		return Config{}, err
	}
	targetUserID, resolvedTargetAgentID, err := b.owner(ctx, targetScope, targetAgentID)
	if err != nil {
		return Config{}, err
	}
	if patch.BinaryVersionsSet {
		typedPayload, err := applyCLIWriteOnlyPatch(def, current.Payload, patch)
		if err != nil {
			return Config{}, err
		}
		patch.PayloadSet = true
		patch.Payload = typedPayload
	}
	updated := current
	updated.Scope = targetScope
	updated.UserID = targetUserID
	updated.AgentID = resolvedTargetAgentID
	if patch.EnabledSet {
		updated.Enabled = patch.Enabled
	}
	updated.Payload, err = patchPayload(current.Payload, patch)
	if err != nil {
		return Config{}, err
	}
	if patch.CredentialRefsSet {
		updated.CredentialRefs = patch.CredentialRefs
	}
	if err := updated.Validate(); err != nil {
		return Config{}, err
	}
	if err := b.service.validateResolved(ctx, def, updated, patch.ResetFields); err != nil {
		return Config{}, err
	}
	result, err := b.service.moveConfigCAS(ctx, current.ID, expectedRevision, updated.Scope, updated.UserID, updated.AgentID, updated.Enabled, updated.Payload, updated.CredentialRefs)
	if err != nil {
		return Config{}, err
	}
	if err := b.service.transitionConfig(ctx, b.authority, MutationMove, def, &current, &result); err != nil {
		return Config{}, err
	}
	return result, nil
}

func (b *Access) DeleteConfig(ctx context.Context, pluginID, id string, expectedRevision int64) error {
	if err := b.ensureActive(); err != nil {
		return err
	}
	if !b.service.txBound {
		return b.service.WithMutationTx(ctx, b.authority, func(mutationCtx context.Context, bound *Access, _ pgx.Tx) error {
			return bound.DeleteConfig(mutationCtx, pluginID, id, expectedRevision)
		})
	}
	current, err := b.GetConfig(ctx, pluginID, id)
	if err != nil {
		return err
	}
	def, err := b.GetDefinition(ctx, current.PluginID)
	if err != nil {
		return err
	}
	if err := ensureActiveDefinition(def); err != nil {
		return err
	}
	if def.Source == SourceBuiltin && current.Scope == ScopeSystem {
		return ErrBuiltinConfig
	}
	deleted, err := b.service.deleteConfigCAS(ctx, id, expectedRevision, current.PluginID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrConflict
	}
	return b.service.transitionConfig(ctx, b.authority, MutationDelete, def, &current, nil)
}

// DeleteMCPServerChild removes one child identity while the enclosing config
// mutation is locked. The caller must first CAS the parent payload, so this
// method cannot delete a child from an unrelated or stale parent.
func (b *Access) DeleteMCPServerChild(ctx context.Context, parentID, childID string) error {
	if err := b.ensureActive(); err != nil {
		return err
	}
	if !b.service.txBound || parentID == "" || childID == "" {
		return ErrConflict
	}
	if err := b.service.q.DeletePluginConfigMCPServer(ctx, sqlc.DeletePluginConfigMCPServerParams{ConfigID: parentID, ID: childID}); err != nil {
		return err
	}
	return nil
}

// CreateMCPServerChild allocates and binds a child UUID in the locked parent
// mutation. Callers receive the server-owned identity and cannot choose a
// credential namespace that belongs to another child.
func (b *Access) CreateMCPServerChild(ctx context.Context, parentID, serverKey string) (MCPServerChild, error) {
	if err := b.ensureActive(); err != nil {
		return MCPServerChild{}, err
	}
	if !b.service.txBound || parentID == "" || serverKey == "" {
		return MCPServerChild{}, ErrConflict
	}
	childID, err := uuid.NewV7()
	if err != nil {
		return MCPServerChild{}, err
	}
	row, err := b.service.q.CreatePluginConfigMCPServer(ctx, sqlc.CreatePluginConfigMCPServerParams{ID: childID.String(), ConfigID: parentID, ServerKey: serverKey})
	if err != nil {
		return MCPServerChild{}, mapConflict(err)
	}
	return MCPServerChild{ID: row.ID, ParentConfigID: row.ConfigID, ServerKey: row.ServerKey, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}, nil
}

func (b *Access) ResetBuiltinConfig(ctx context.Context, pluginID, id string, expectedRevision int64) (Config, error) {
	if err := b.ensureActive(); err != nil {
		return Config{}, err
	}
	if !b.service.txBound {
		var reset Config
		err := b.service.WithMutationTx(ctx, b.authority, func(mutationCtx context.Context, bound *Access, _ pgx.Tx) error {
			var err error
			reset, err = bound.ResetBuiltinConfig(mutationCtx, pluginID, id, expectedRevision)
			return err
		})
		return reset, err
	}
	current, err := b.GetConfig(ctx, pluginID, id)
	if err != nil {
		return Config{}, err
	}
	def, err := b.GetDefinition(ctx, current.PluginID)
	if err != nil {
		return Config{}, err
	}
	if err := ensureActiveDefinition(def); err != nil {
		return Config{}, err
	}
	if !b.authority.IsAdmin() || def.Source != SourceBuiltin || current.Scope != ScopeSystem {
		return Config{}, ErrForbidden
	}
	reset, err := b.service.resetBuiltinConfig(ctx, id, expectedRevision, current.PluginID)
	if err != nil {
		return Config{}, err
	}
	if err := b.service.transitionConfig(ctx, b.authority, MutationReset, def, &current, &reset); err != nil {
		return Config{}, err
	}
	return reset, nil
}

// SyncBuiltinDefaults is one startup transaction. Existing config identities,
// explicit decisions, pins and timestamps are never rewritten by a release.
func (s *Service) SyncBuiltinDefaults(ctx context.Context) error {
	if s == nil || s.db == nil || s.mutationFence == nil || ctx == nil {
		return ErrForbidden
	}
	if mutationInProgress(ctx) || s.txBound {
		return ErrNestedMutation
	}
	return s.mutationFence(ctx, func() error { return s.syncBuiltinDefaultsTx(ctx) })
}

func (s *Service) syncBuiltinDefaultsTx(ctx context.Context) error {
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
		_, err := q.UpsertPluginDefinition(ctx, sqlc.UpsertPluginDefinitionParams{
			ID: def.ID, DisplayName: def.DisplayName,
			Source: string(def.Source), Spec: def.Spec, DefaultEnabled: def.DefaultEnabled, Revision: def.Revision,
		})
		if err != nil {
			return fmt.Errorf("sync definition %s: %w", def.ID, err)
		}
		row, err := q.EnsureSystemPluginConfig(ctx, sqlc.EnsureSystemPluginConfigParams{PluginID: def.ID, Config: json.RawMessage(`{}`)})
		if err != nil {
			return fmt.Errorf("sync config %s: %w", def.ID, err)
		}
		// Fixed package resources have stable child identities even when the
		// system config inherits its payload from the definition. Reconcile these
		// rows during startup so settings can address each inherited server.
		var declaration ResourcePayload
		err = json.Unmarshal(def.Spec, &declaration)
		if err != nil {
			return fmt.Errorf("sync definition %s: decode spec: %w", def.ID, err)
		}
		if declaration.Origin != "remote_mcp" && len(declaration.MCPServers) != 0 {
			config := fromSQLConfig(sqlc.PluginConfig(row))
			// Startup sync must preserve an existing projection even when it was
			// written by an older release and no longer satisfies the formal
			// parameter contract. Child reconciliation is safe only for payloads
			// that can be resolved against the current declaration.
			if _, resolveErr := MergeDefinitionConfig(def.Spec, config.Payload); resolveErr != nil {
				continue
			}
			if err := (&Service{q: q}).ensureMCPServerChildren(ctx, &config); err != nil {
				return fmt.Errorf("sync MCP children %s: %w", def.ID, err)
			}
		}
	}
	return classifyCommitError(tx.Commit(ctx))
}

func (b *Access) CreateCustom(ctx context.Context, def Definition, config Config) (Definition, Config, error) {
	return b.createCustom(ctx, def, config, false)
}

// createCustom carries the only internal exception to the public custom
// definition boundary. A true packagePublished value is supplied only after
// ContentStore has published and validated the package tree.
func (b *Access) createCustom(ctx context.Context, def Definition, config Config, packagePublished bool) (Definition, Config, error) {
	if err := b.ensureActive(); err != nil {
		return Definition{}, Config{}, err
	}
	if !b.service.txBound {
		var createdDef Definition
		var createdConfig Config
		err := b.service.WithMutationTx(ctx, b.authority, func(mutationCtx context.Context, bound *Access, _ pgx.Tx) error {
			var err error
			createdDef, createdConfig, err = bound.createCustom(mutationCtx, def, config, packagePublished)
			return err
		})
		return createdDef, createdConfig, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Definition{}, Config{}, err
	}
	def.Source, def.Revision = SourceCustom, 1
	def.CreatorUserID, def.DefaultEnabled = string(b.authority.UserID()), false
	if b.authority.IsAdmin() {
		def.CreatorUserID = ""
	}
	def.Spec = nonEmptyJSON(def.Spec)
	def.Spec, err = PublishDefinitionSpec(def.Spec)
	if err != nil {
		return Definition{}, Config{}, err
	}
	// Package content is trusted only after the directory publisher has copied,
	// validated, and content-addressed the tree. The public metadata API must
	// never let a caller claim an arbitrary digest and bundled skill payload.
	if isPackageDefinition(def) && !packagePublished {
		return Definition{}, Config{}, fmt.Errorf("%w: package definitions require the directory publisher", ErrInvalidDefinition)
	}
	if err := validateCustomSpec(def); err != nil {
		return Definition{}, Config{}, err
	}
	userID, agentID, err := b.owner(ctx, config.Scope, config.AgentID)
	if err != nil {
		return Definition{}, Config{}, err
	}
	config.ID, config.PluginID = id.String(), def.ID
	config.UserID, config.AgentID, config.Revision = userID, agentID, 1
	config.CredentialRefs = nonEmptyJSON(config.CredentialRefs)
	if err := config.Validate(); err != nil {
		return Definition{}, Config{}, err
	}
	if err := validateCustomResourceContent(def, config, b.authority.IsAdmin()); err != nil {
		return Definition{}, Config{}, err
	}
	if err := rejectImmutableSkillPayload(config.Payload); err != nil {
		return Definition{}, Config{}, err
	}
	if config.Enabled == nil || !*config.Enabled {
		if b.service.policy.Validate == nil {
			return Definition{}, Config{}, ErrInvalidDefinition
		}
		definitionOnly := cloneConfig(config)
		disabled := false
		definitionOnly.Payload, definitionOnly.Enabled = nil, &disabled
		if err := b.service.policy.Validate(ctx, def, definitionOnly, nil); err != nil {
			return Definition{}, Config{}, err
		}
	}
	if err := b.service.validateResolved(ctx, def, config, nil); err != nil {
		return Definition{}, Config{}, err
	}
	createdDef, createdConfig, err := b.service.createCustom(ctx, def, config)
	if err != nil {
		return Definition{}, Config{}, err
	}
	if err := b.service.transitionConfig(ctx, b.authority, MutationCreate, createdDef, nil, &createdConfig); err != nil {
		return Definition{}, Config{}, err
	}
	return createdDef, createdConfig, nil
}

func isPackageDefinition(def Definition) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(def.Spec, &fields); err != nil || fields == nil {
		return false
	}
	var origin string
	if err := json.Unmarshal(fields["origin"], &origin); err != nil {
		return false
	}
	return origin == "package"
}

// validateCustomResourceContent preserves the remote-only non-admin boundary
// from the actual resources rather than a caller-selected backend label.
func validateCustomResourceContent(def Definition, config Config, isAdmin bool) error {
	if isAdmin {
		return nil
	}
	for label, raw := range map[string]json.RawMessage{"definition": def.Spec, "config": config.Payload} {
		var fields map[string]json.RawMessage
		if len(raw) == 0 {
			continue
		}
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return fmt.Errorf("%w: %s resource must be an object", ErrInvalidConfig, label)
		}
		for key := range fields {
			switch key {
			case "description", "version", contentDigestField, "origin", "mcp_servers", "url", "transport", "auth_type", "credential_mode", "metadata":
				continue
			default:
				return fmt.Errorf("%w: non-admin custom resources cannot declare %s", ErrForbidden, key)
			}
		}
	}
	return nil
}

// Custom packages cannot claim shipped Skill bytes. Resource consumers validate
// the typed declarations after this small authored-field boundary.
func validateCustomSpec(def Definition) error {
	if err := def.Validate(); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(def.Spec, &fields); err != nil {
		return ErrInvalidDefinition
	}
	packageContent := false
	if origin, ok := fields["origin"]; ok {
		var value string
		if err := json.Unmarshal(origin, &value); err != nil {
			return ErrInvalidDefinition
		}
		packageContent = value == "package"
		if packageContent {
			if _, ok := fields["content"]; !ok {
				return fmt.Errorf("%w: package definitions require content", ErrInvalidDefinition)
			}
		}
	}
	if !packageContent {
		if _, ok := fields["content"]; ok {
			return fmt.Errorf("%w: custom definitions cannot claim bundled content", ErrInvalidDefinition)
		}
	}
	for field, value := range fields {
		if field == "description" {
			var description string
			if err := json.Unmarshal(value, &description); err != nil {
				return ErrInvalidDefinition
			}
			continue
		}
		switch field {
		case "category", "prompt", "version", "content_digest", "content", "origin", "binaries", "session_env", "oauth_provider", "oauth", "mcp_servers":
			continue
		case "skills":
			var skills []json.RawMessage
			if err := json.Unmarshal(value, &skills); err != nil || (!packageContent && len(skills) != 0) {
				return fmt.Errorf("%w: custom definitions cannot claim bundled skills", ErrInvalidDefinition)
			}
			continue
		}
		return fmt.Errorf("%w: unsupported definition field %s", ErrInvalidDefinition, field)
	}
	return nil
}

func (s *Service) validateResolved(ctx context.Context, def Definition, config Config, resetFields []string) error {
	if err := config.Validate(); err != nil {
		return err
	}
	enabled := def.DefaultEnabled
	if config.Enabled != nil {
		enabled = *config.Enabled
	}
	if len(config.Payload) == 0 {
		return nil
	}
	if err := rejectImmutableSkillPayload(config.Payload); err != nil {
		return err
	}
	// Only fixed upper bounds can suppress validation here: user configs may
	// later execute on many Agents, so an unrelated Agent cap is irrelevant.
	for _, scope := range []Scope{ScopeSystem, ScopeSystemAgent} {
		if config.Scope == scope {
			continue
		}
		agentID := ""
		if scope == ScopeSystemAgent {
			if config.AgentID == "" {
				continue
			}
			agentID = config.AgentID
		}
		rows, err := s.q.ListPluginConfigsOwned(ctx, sqlc.ListPluginConfigsOwnedParams{
			PluginID: def.ID, Scope: string(scope), AgentID: nullableText(agentID),
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Enabled.Valid && !row.Enabled.Bool {
				enabled = false
			}
		}
	}
	merged, err := MergeDefinitionConfig(def.Spec, config.Payload)
	if err != nil {
		return fmt.Errorf("%w: resolved payload: %w", ErrInvalidConfig, err)
	}
	if s.policy.Validate == nil {
		return fmt.Errorf("%w: backend validator unavailable", ErrInvalidConfig)
	}
	resolved := cloneConfig(config)
	resolved.Payload, resolved.Enabled = merged, &enabled
	return s.policy.Validate(ctx, def, resolved, resetFields)
}

func rejectImmutableSkillPayload(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("%w: payload must be a JSON object", ErrInvalidConfig)
	}
	if _, exists := fields["skills"]; exists {
		return fmt.Errorf("%w: bundled skill membership is immutable", ErrInvalidConfig)
	}
	return nil
}

func rejectImmutableSkillPatch(patch ConfigPatch) error {
	if slices.Contains(patch.ResetFields, "skills") {
		return fmt.Errorf("%w: bundled skill membership is immutable", ErrInvalidConfig)
	}
	if !patch.PayloadSet {
		return nil
	}
	return rejectImmutableSkillPayload(patch.Payload)
}

func (s *Service) transitionConfig(ctx context.Context, authority authz.Authority, kind MutationKind, def Definition, before, after *Config) error {
	if s.policy.Transition == nil {
		return fmt.Errorf("%w: backend transition unavailable", ErrInvalidConfig)
	}
	if !s.txBound || s.mutationTx == nil {
		return ErrForbidden
	}
	var beforeClone, afterClone *Config
	if before != nil {
		copy := cloneConfig(*before)
		beforeClone = &copy
	}
	if after != nil {
		copy := cloneConfig(*after)
		afterClone = &copy
	}
	return s.policy.Transition(ctx, s.mutationTx, authority, kind, cloneDefinition(def), beforeClone, afterClone)
}

func patchPayload(current json.RawMessage, patch ConfigPatch) (json.RawMessage, error) {
	if len(current) == 0 && len(patch.ResetFields) != 0 {
		return nil, ErrInvalidConfig
	}
	if !patch.PayloadSet && len(patch.ResetFields) == 0 {
		return cloneRaw(current), nil
	}
	if patch.PayloadSet && len(patch.Payload) == 0 {
		if len(patch.ResetFields) != 0 {
			return nil, ErrInvalidConfig
		}
		return nil, nil
	}
	var owned map[string]json.RawMessage
	if err := json.Unmarshal(nonEmptyJSON(current), &owned); err != nil || owned == nil {
		return nil, ErrInvalidConfig
	}
	var fields map[string]json.RawMessage
	if patch.PayloadSet {
		if err := json.Unmarshal(patch.Payload, &fields); err != nil || fields == nil {
			return nil, ErrInvalidConfig
		}
		maps.Copy(owned, fields)
	}
	for _, key := range patch.ResetFields {
		if key == "" {
			return nil, ErrInvalidConfig
		}
		if _, supplied := fields[key]; supplied {
			return nil, fmt.Errorf("%w: patch and reset overlap", ErrInvalidConfig)
		}
		delete(owned, key)
	}
	return json.Marshal(owned)
}

// DefinitionPatch changes presentation metadata only; execution identity and
// resource declarations cannot be replaced under existing shared configs.
type DefinitionPatch struct {
	DisplayName *string
	Description *string
}

func (b *Access) managedDefinition(ctx context.Context, id string) (Definition, error) {
	def, err := b.service.getDefinition(ctx, id)
	if err != nil {
		return Definition{}, err
	}
	if def.Source == SourceBuiltin {
		return Definition{}, ErrForbidden
	}
	if !b.authority.IsAdmin() && (def.CreatorUserID == "" || def.CreatorUserID != string(b.authority.UserID())) {
		return Definition{}, ErrNotFound
	}
	if !def.RetiredAt.IsZero() {
		return Definition{}, ErrRetiredDefinition
	}
	return def, nil
}

func ensureActiveDefinition(def Definition) error {
	if !def.RetiredAt.IsZero() {
		return ErrRetiredDefinition
	}
	return nil
}

func (b *Access) UpdateDefinition(ctx context.Context, id string, revision int64, patch DefinitionPatch) (Definition, error) {
	if err := b.ensureActive(); err != nil {
		return Definition{}, err
	}
	if !b.service.txBound {
		var updated Definition
		err := b.service.WithMutationTx(ctx, b.authority, func(mutationCtx context.Context, bound *Access, _ pgx.Tx) error {
			var err error
			updated, err = bound.UpdateDefinition(mutationCtx, id, revision, patch)
			return err
		})
		return updated, err
	}
	def, err := b.managedDefinition(ctx, id)
	if err != nil {
		return Definition{}, err
	}
	if patch.DisplayName != nil {
		def.DisplayName = *patch.DisplayName
	}
	if patch.Description != nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(def.Spec, &fields); err != nil {
			return Definition{}, err
		}
		fields["description"], err = json.Marshal(*patch.Description)
		if err != nil {
			return Definition{}, err
		}
		encoded, marshalErr := json.Marshal(fields)
		if marshalErr != nil {
			return Definition{}, marshalErr
		}
		def.Spec, err = PublishDefinitionSpec(encoded)
		if err != nil {
			return Definition{}, err
		}
	}
	if err := validateCustomSpec(def); err != nil {
		return Definition{}, err
	}
	row, err := b.service.q.UpdatePluginDefinitionCAS(ctx, sqlc.UpdatePluginDefinitionCASParams{
		ID: id, Revision: revision, DisplayName: def.DisplayName, Spec: def.Spec,
	})
	if err != nil {
		return Definition{}, mapConflict(err)
	}
	return fromSQLDefinition(row), nil
}

func (b *Access) DeleteDefinition(ctx context.Context, id string, revision int64) error {
	if err := b.ensureActive(); err != nil {
		return err
	}
	if !b.service.txBound {
		return b.service.WithMutationTx(ctx, b.authority, func(mutationCtx context.Context, bound *Access, _ pgx.Tx) error {
			return bound.DeleteDefinition(mutationCtx, id, revision)
		})
	}
	if _, err := b.managedDefinition(ctx, id); err != nil {
		return err
	}
	return b.service.retireDefinition(ctx, id, revision)
}

func (s *Service) retireDefinition(ctx context.Context, id string, revision int64) error {
	_, err := s.q.RetirePluginDefinitionCAS(ctx, sqlc.RetirePluginDefinitionCASParams{ID: id, Revision: revision})
	if err != nil {
		return mapConflict(err)
	}
	return nil
}
