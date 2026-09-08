package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// PackagePreview is a safe, read-only summary of a candidate package. It
// deliberately contains declarations and impact scope only, never package
// bytes, endpoint headers, credentials, or filesystem paths.
type PackagePreview struct {
	PluginID           string
	CurrentRevision    int64
	CandidateDigest    string
	CandidateVersion   string
	OAuthChanges       []OAuthPreviewChange
	BinaryNames        []string
	SkillNames         []string
	IncompatibleScopes []Scope
}

type OAuthPreviewChange struct {
	Provider                       string
	AddedScopes, RemovedScopes     []string
	AddedBindings, RemovedBindings []string
}

// PreviewDefinitionFromDirectory validates a coherent, bounded snapshot of a
// candidate package. Publication only creates an immutable content-addressed
// candidate; it does not change the definition or any config. The digest is
// returned so update can reject a directory that changed after preview.
func (b *Access) PreviewDefinitionFromDirectory(ctx context.Context, id string, revision int64, source string) (PackagePreview, error) {
	if err := b.ensureActive(); err != nil {
		return PackagePreview{}, err
	}
	if !b.authority.IsAdmin() {
		return PackagePreview{}, ErrForbidden
	}
	if b.service.contentStore == nil {
		return PackagePreview{}, fmt.Errorf("%w: package content store is not configured", ErrInvalidDefinition)
	}
	current, err := b.managedDefinition(ctx, id)
	if err != nil {
		return PackagePreview{}, err
	}
	if current.Revision != revision {
		return PackagePreview{}, ErrConflict
	}
	var preview PackagePreview
	err = b.service.contentStore.withPublished(source, func(published agentpackage.PublishedPackage) error {
		preview, err = b.previewPublishedPackage(ctx, id, current, published)
		return err
	})
	return preview, err
}

func (b *Access) previewPublishedPackage(ctx context.Context, id string, current Definition, published agentpackage.PublishedPackage) (PackagePreview, error) {
	candidate := published.Package
	if candidate == nil {
		return PackagePreview{}, fmt.Errorf("%w: published package is empty", ErrInvalidDefinition)
	}
	if candidate.Manifest.Name != id {
		return PackagePreview{}, fmt.Errorf("%w: package name cannot replace requested definition", ErrInvalidDefinition)
	}
	def, err := ResourcePayloadFromAgentPackage(candidate)
	if err != nil {
		return PackagePreview{}, err
	}
	preview := PackagePreview{PluginID: id, CurrentRevision: current.Revision, CandidateDigest: published.Digest, CandidateVersion: candidate.Manifest.Version}
	currentPayload, err := DecodeResourcePayload(current.Spec, "current definition spec")
	if err != nil {
		return PackagePreview{}, fmt.Errorf("decode current definition: %w", err)
	}
	preview.OAuthChanges = oauthPreviewChanges(currentPayload.OAuth, def.OAuth)
	for _, binary := range def.Binaries {
		preview.BinaryNames = append(preview.BinaryNames, binary.Name)
	}
	for _, skill := range def.Skills {
		preview.SkillNames = append(preview.SkillNames, skill.Name)
	}
	slices.Sort(preview.BinaryNames)
	slices.Sort(preview.SkillNames)
	configs, err := b.service.q.ListPluginConfigs(ctx, id)
	if err != nil {
		return PackagePreview{}, err
	}
	candidateSpec, err := previewDefinitionSpec(def, published.Digest)
	if err != nil {
		return PackagePreview{}, err
	}
	candidateDef := Definition{ID: id, DisplayName: current.DisplayName, Source: current.Source, Spec: candidateSpec, Revision: current.Revision}
	if err := validateCustomSpec(candidateDef); err != nil {
		return PackagePreview{}, err
	}
	if err := ValidateResourceDeclarations(def, "package "+id, nil); err != nil {
		return PackagePreview{}, err
	}
	for _, row := range configs {
		config := fromSQLConfig(row)
		if err := ValidatePayload(ctx, candidateDef, config, nil); err != nil {
			preview.IncompatibleScopes = appendUniqueScope(preview.IncompatibleScopes, config.Scope)
		}
	}
	slices.SortFunc(preview.IncompatibleScopes, func(a, b Scope) int { return strings.Compare(string(a), string(b)) })
	return preview, nil
}

func previewDefinitionSpec(payload ResourcePayload, packageDigest string) (json.RawMessage, error) {
	payload.Content = &ContentReference{Digest: packageDigest}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode preview definition: %w", err)
	}
	spec, err := PublishDefinitionSpec(encoded)
	if err != nil {
		return nil, fmt.Errorf("publish preview definition: %w", err)
	}
	return spec, nil
}

func appendUniqueScope(scopes []Scope, scope Scope) []Scope {
	if slices.Contains(scopes, scope) {
		return scopes
	}
	return append(scopes, scope)
}

func oauthPreviewChanges(before, after []OAuthRequirement) []OAuthPreviewChange {
	byProvider := func(items []OAuthRequirement) map[string]OAuthRequirement {
		out := make(map[string]OAuthRequirement)
		for _, item := range items {
			out[item.Provider] = item
		}
		return out
	}
	left, right := byProvider(before), byProvider(after)
	providers := make(map[string]struct{})
	for key := range left {
		providers[key] = struct{}{}
	}
	for key := range right {
		providers[key] = struct{}{}
	}
	changes := make([]OAuthPreviewChange, 0, len(providers))
	for provider := range providers {
		l := left[provider]
		r := right[provider]
		change := OAuthPreviewChange{Provider: provider}
		change.AddedScopes = diffStrings(r.Scopes, l.Scopes)
		change.RemovedScopes = diffStrings(l.Scopes, r.Scopes)
		change.AddedBindings = oauthBindingDiff(r.Bindings, l.Bindings)
		change.RemovedBindings = oauthBindingDiff(l.Bindings, r.Bindings)
		if len(change.AddedScopes)+len(change.RemovedScopes)+len(change.AddedBindings)+len(change.RemovedBindings) > 0 {
			changes = append(changes, change)
		}
	}
	slices.SortFunc(changes, func(a, b OAuthPreviewChange) int { return strings.Compare(a.Provider, b.Provider) })
	return changes
}

func diffStrings(after, before []string) []string {
	out := make([]string, 0)
	for _, value := range after {
		if !slices.Contains(before, value) {
			out = append(out, value)
		}
	}
	slices.Sort(out)
	return out
}

func oauthBindingDiff(after, before []OAuthBinding) []string {
	out := make([]string, 0)
	for _, value := range after {
		key := value.Credential + "=" + value.EnvVar + "=" + value.Connection
		found := false
		for _, prior := range before {
			if key == prior.Credential+"="+prior.EnvVar+"="+prior.Connection {
				found = true
				break
			}
		}
		if !found {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

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
func (b *Access) UpdateDefinitionFromDirectory(ctx context.Context, id string, revision int64, source, expectedDigest string) (Definition, error) {
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
		if published.Digest != expectedDigest {
			return ErrConflict
		}
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
