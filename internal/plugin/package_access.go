package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// CreateCustomFromDirectory is the administrator package-install boundary.
// Files are validated and published before the normal Access transaction is
// entered, so a failed CAS cannot expose a half-written package.
func (b *Access) CreateCustomFromDirectory(ctx context.Context, source string, config Config) (Definition, Config, error) {
	if err := b.ensureActive(); err != nil {
		return Definition{}, Config{}, err
	}
	if !b.authority.IsAdmin() {
		return Definition{}, Config{}, ErrForbidden
	}
	if b.service.txBound {
		return Definition{}, Config{}, ErrNestedMutation
	}
	if config.Enabled == nil && len(config.Payload) == 0 {
		disabled := false
		config.Enabled = &disabled
	}
	var created Definition
	var createdConfig Config
	err := b.service.withPublishedAgentPackage(source, func(published agentpackage.PublishedPackage) error {
		def, err := definitionFromPublishedPackage(published)
		if err != nil {
			return err
		}
		created, createdConfig, err = b.createCustom(ctx, def, config, true)
		return err
	})
	return created, createdConfig, err
}

// UpdateDefinitionFromDirectory stages a complete package version and then
// swaps only the definition CAS. Existing config IDs, credentials, and MCP
// child rows remain untouched; configs that cannot satisfy the new declaration
// become explicitly unavailable until repaired.
func (b *Access) UpdateDefinitionFromDirectory(ctx context.Context, id string, revision int64, source string) (Definition, error) {
	if err := b.ensureActive(); err != nil {
		return Definition{}, err
	}
	if !b.authority.IsAdmin() {
		return Definition{}, ErrForbidden
	}
	if b.service.txBound {
		return Definition{}, ErrNestedMutation
	}
	var updated Definition
	err := b.service.withPublishedAgentPackage(source, func(published agentpackage.PublishedPackage) error {
		def, err := definitionFromPublishedPackage(published)
		if err != nil {
			return err
		}
		return b.service.WithMutationTx(ctx, b.authority, func(mutationCtx context.Context, bound *Access, _ pgx.Tx) error {
			var err error
			updated, err = bound.updatePublishedDefinition(mutationCtx, id, revision, def)
			return err
		})
	})
	return updated, err
}

func (b *Access) updatePublishedDefinition(ctx context.Context, id string, revision int64, def Definition) (Definition, error) {
	current, err := b.managedDefinition(ctx, id)
	if err != nil {
		return Definition{}, err
	}
	if def.ID != current.ID {
		return Definition{}, fmt.Errorf("%w: package name %q cannot replace %q", ErrInvalidDefinition, def.ID, current.ID)
	}
	if err := validateCustomSpec(def); err != nil {
		return Definition{}, err
	}
	payload, err := DecodeResourcePayload(def.Spec, "package "+def.ID)
	if err != nil {
		return Definition{}, err
	}
	if err := ValidateResourceDeclarations(payload, "package "+def.ID, nil); err != nil {
		return Definition{}, err
	}
	def.DisplayName = current.DisplayName
	def.Source = current.Source
	def.CreatorUserID = current.CreatorUserID
	def.DefaultEnabled = current.DefaultEnabled
	def.Revision = current.Revision
	row, err := b.service.q.UpdatePluginDefinitionCAS(ctx, sqlc.UpdatePluginDefinitionCASParams{
		ID: id, Revision: revision, DisplayName: def.DisplayName, Spec: def.Spec,
	})
	if err != nil {
		return Definition{}, mapConflict(err)
	}
	return fromSQLDefinition(row), nil
}

func (s *Service) withPublishedAgentPackage(source string, fn func(agentpackage.PublishedPackage) error) error {
	if s == nil || s.contentStore == nil {
		return fmt.Errorf("%w: package content store is not configured", ErrInvalidDefinition)
	}
	return s.contentStore.withPublished(source, fn)
}

func definitionFromPublishedPackage(published agentpackage.PublishedPackage) (Definition, error) {
	if published.Package == nil {
		return Definition{}, fmt.Errorf("%w: published package is empty", ErrInvalidDefinition)
	}
	payload, err := ResourcePayloadFromAgentPackage(published.Package)
	if err != nil {
		return Definition{}, err
	}
	payload.Content = &ContentReference{Digest: published.Digest}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Definition{}, fmt.Errorf("encode package declaration: %w", err)
	}
	spec, err := PublishDefinitionSpec(encoded)
	if err != nil {
		return Definition{}, err
	}
	displayName := published.Package.Manifest.Name
	if published.Package.Extension != nil && published.Package.Extension.DisplayName != "" {
		displayName = published.Package.Extension.DisplayName
	}
	return Definition{ID: published.Package.Manifest.Name, DisplayName: displayName, Source: SourceCustom, Spec: spec, Revision: 1}, nil
}
