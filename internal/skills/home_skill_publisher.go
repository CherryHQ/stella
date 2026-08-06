package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

const homeSkillPublishStripeCount = 257

// ErrHomeSkillConflict means the direct catalog entry no longer satisfies the
// request's expected digest. It is safe for callers to present as a conflict:
// no managed publication was attempted.
var ErrHomeSkillConflict = errors.New("skills: Home Skill publication conflict")

// HomeSkillFilesystemAccess is the sole Home capability managed publication
// needs. It deliberately grants a callback over an exact typed Skill root,
// never a Home record, attachment, locator, or host path.
type HomeSkillFilesystemAccess interface {
	UseSkillFilesystem(context.Context, *home.SkillRoot, func(sandbox.Filesystem) error) error
}

// HomeSkillMetadata is the canonical filesystem metadata owned by Stella.
// CreatedBy must live in Metadata so it remains part of the revision digest.
type HomeSkillMetadata struct {
	Status                 string
	DisableModelInvocation bool
	Metadata               map[string]any
	CreatedAt              time.Time
	UpdatedAt              time.Time
	LegacyLifecycleVersion int64
}

// HomeSkillFile is one opaque regular file in a complete Skill tree.
type HomeSkillFile struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

// HomeSkillPublishRequest names one entry in an exact typed Skill root.
// ExpectedDigest is an optimistic concurrency token: empty creates only an
// absent direct entry; otherwise it must be the exact current managed digest.
type HomeSkillPublishRequest struct {
	Root           *home.SkillRoot
	Name           string
	ExpectedDigest string
	Metadata       HomeSkillMetadata
	Files          []HomeSkillFile
}

// HomeSkillUnpublishRequest withdraws one exact managed selection from a typed
// Home Skill root. Its immutable revision remains retained for later GC.
type HomeSkillUnpublishRequest struct {
	Root           *home.SkillRoot
	Name           string
	ExpectedDigest string
}

// HomeSkillPublisher publishes complete, digest-pinned revisions to all four
// typed Skill roots. Stripes are process-local for the Phase-1 single-replica
// ceiling; replace them with Phase-4 PG advisory locking when writers can run
// in more than one replica.
//
// SkillRoot intentionally does not expose its owner or a stable identifier.
// The bounded stripes therefore serialize same-name managed writes across
// roots conservatively; root isolation is enforced by the typed callback.
type HomeSkillPublisher struct {
	homes HomeSkillFilesystemAccess
	core  managedSkillPublicationCore

	revisionTelemetry *RevisionTelemetry
	catalogRoot       func(*home.SkillRoot) (FilesystemCatalogRoot, error)
}

func NewHomeSkillPublisher(homes HomeSkillFilesystemAccess) (*HomeSkillPublisher, error) {
	if homes == nil {
		return nil, errors.New("skills: Home Skill filesystem access is required")
	}
	return &HomeSkillPublisher{homes: homes, core: newManagedSkillPublicationCore()}, nil
}

// NewHomeSkillPublisherWithRevisionTelemetry adds best-effort retained
// revision observation after verified publication. The resolver remains opaque:
// it maps an already validated typed root to a catalog root without exposing a
// Home record, attachment, locator, or host path.
func NewHomeSkillPublisherWithRevisionTelemetry(homes HomeSkillFilesystemAccess, telemetry *RevisionTelemetry, catalogRoot func(*home.SkillRoot) (FilesystemCatalogRoot, error)) (*HomeSkillPublisher, error) {
	publisher, err := NewHomeSkillPublisher(homes)
	if err != nil {
		return nil, err
	}
	if telemetry == nil || catalogRoot == nil {
		return nil, errors.New("skills: revision telemetry and catalog root resolver are required together")
	}
	publisher.revisionTelemetry = telemetry
	publisher.catalogRoot = catalogRoot
	return publisher, nil
}

// Publish validates and snapshots the complete caller-owned request before it
// opens Home bytes, then publishes it at the selected root's workspace.
func (p *HomeSkillPublisher) Publish(ctx context.Context, request HomeSkillPublishRequest) (string, error) {
	if p == nil || p.homes == nil {
		return "", errors.New("skills: Home Skill publisher is unavailable")
	}
	return p.core.publish(ctx, request, p.homes.UseSkillFilesystem, p.observeVerifiedPublication)
}

// Unpublish removes only the direct link that still selects ExpectedDigest.
// It shares publication's bounded writer stripes, serializing managed
// create/update/delete for one logical name in the Phase-1 single-replica
// ceiling. Same-UID arbitrary POSIX writers retain ordinary winner semantics.
func (p *HomeSkillPublisher) Unpublish(ctx context.Context, request HomeSkillUnpublishRequest) error {
	if p == nil || p.homes == nil {
		return errors.New("skills: Home Skill publisher is unavailable")
	}
	return p.core.unpublish(ctx, request, p.homes.UseSkillFilesystem)
}

func (p *HomeSkillPublisher) observeVerifiedPublication(ctx context.Context, root *home.SkillRoot, filesystem sandbox.Filesystem) {
	if p.revisionTelemetry == nil {
		return
	}
	catalogRoot, err := p.catalogRoot(root)
	if err != nil {
		slog.Warn("Home Skill revision telemetry failed after publication", "reason", "root_unavailable")
		return
	}
	// Observe logs path-safe collection failures itself. A verified publication
	// remains successful regardless.
	_ = p.revisionTelemetry.Observe(ctx, filesystem, catalogRoot)
}

// managedSkillPublicationCore owns the publication state machine shared by
// typed Home publication and the legacy shared-only compatibility adapter.
type managedSkillPublicationCore struct {
	stripes [homeSkillPublishStripeCount]chan struct{}
}

func newManagedSkillPublicationCore() managedSkillPublicationCore {
	var core managedSkillPublicationCore
	for i := range core.stripes {
		core.stripes[i] = make(chan struct{}, 1)
		core.stripes[i] <- struct{}{}
	}
	return core
}

type (
	useHomeSkillFilesystem         func(context.Context, *home.SkillRoot, func(sandbox.Filesystem) error) error
	observeManagedSkillPublication func(context.Context, *home.SkillRoot, sandbox.Filesystem)
)

func (p *managedSkillPublicationCore) publish(ctx context.Context, request HomeSkillPublishRequest, use useHomeSkillFilesystem, observe observeManagedSkillPublication) (string, error) {
	if p == nil || use == nil {
		return "", errors.New("skills: Home Skill publisher is unavailable")
	}
	if ctx == nil {
		return "", errors.New("skills: Home Skill publication context is required")
	}
	tree, err := snapshotHomeSkillRequest(request)
	if err != nil {
		return "", err
	}
	if err := validateHomeSkillPublicationBounds(tree); err != nil {
		return "", err
	}
	digest, err := digestSkillTree(tree)
	if err != nil {
		return "", fmt.Errorf("skills: validate Home Skill tree: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	stripe := p.stripes[homeSkillPublishStripe(request.Name)]
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-stripe:
	}
	defer func() { stripe <- struct{}{} }()

	publicationInvoked := false
	err = use(ctx, request.Root, func(filesystem sandbox.Filesystem) error {
		inspector, ok := filesystem.(sandbox.ManagedSkillTargetInspector)
		if !ok {
			return errors.New("skills: filesystem does not support managed Skill inspection")
		}
		publisher, ok := filesystem.(sandbox.ManagedSkillPublisher)
		if !ok {
			return errors.New("skills: filesystem does not support managed Skill publication")
		}
		entry := path.Join(sandbox.PathWorkspace, request.Name)
		target, err := inspector.InspectManagedSkillTarget(ctx, entry)
		if err != nil {
			if errors.Is(err, sandbox.ErrOutcomeUnknown) {
				return err
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// A malformed or foreign symlink is an ordinary occupant for this
			// authority. It is never a candidate for replacement.
			return fmt.Errorf("%w: inspect direct entry %q: %w", ErrHomeSkillConflict, request.Name, err)
		}
		if err := checkHomeSkillExpected(ctx, filesystem, entry, request.Name, request.ExpectedDigest, target); err != nil {
			return err
		}

		publicationInvoked = true
		if err := publisher.PublishManagedSkill(ctx, sandbox.PathWorkspace, request.Name, digest, skillTreePublication(tree)); err != nil {
			// Once the publisher has been invoked, it may have selected a
			// revision even when it reports an ordinary failure. A retry or
			// clean conflict would therefore lie about the observable outcome.
			return fmt.Errorf("%w: publish Home Skill %q: %w", sandbox.ErrOutcomeUnknown, request.Name, err)
		}
		selected, err := inspector.InspectManagedSkillTarget(ctx, entry)
		if err != nil {
			return fmt.Errorf("%w: verify selected Home Skill %q: %w", sandbox.ErrOutcomeUnknown, request.Name, err)
		}
		if !selected.Managed || selected.Digest != digest {
			return fmt.Errorf("%w: selected digest %q, want %q", sandbox.ErrOutcomeUnknown, selected.Digest, digest)
		}
		if observe != nil {
			observe(ctx, request.Root, filesystem)
		}
		return nil
	})
	if err != nil {
		if publicationInvoked && !errors.Is(err, sandbox.ErrOutcomeUnknown) {
			return "", fmt.Errorf("%w: finalize Home Skill publication: %w", sandbox.ErrOutcomeUnknown, err)
		}
		return "", err
	}
	return digest, nil
}

func (p *managedSkillPublicationCore) unpublish(ctx context.Context, request HomeSkillUnpublishRequest, use useHomeSkillFilesystem) error {
	if p == nil || use == nil {
		return errors.New("skills: Home Skill publisher is unavailable")
	}
	if ctx == nil {
		return errors.New("skills: Home Skill unpublication context is required")
	}
	if err := snapshotHomeSkillUnpublishRequest(request); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	stripe := p.stripes[homeSkillPublishStripe(request.Name)]
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-stripe:
	}
	defer func() { stripe <- struct{}{} }()

	primitiveConflict := false
	primitiveMayHaveMutated := false
	err := use(ctx, request.Root, func(filesystem sandbox.Filesystem) error {
		unpublisher, ok := filesystem.(sandbox.ManagedSkillUnpublisher)
		if !ok {
			return errors.New("skills: filesystem does not support managed Skill unpublication")
		}
		primitiveMayHaveMutated = true
		if err := unpublisher.UnpublishManagedSkill(ctx, sandbox.PathWorkspace, request.Name, request.ExpectedDigest); err != nil {
			if errors.Is(err, sandbox.ErrOutcomeUnknown) {
				return err
			}
			if errors.Is(err, sandbox.ErrManagedSkillConflict) {
				primitiveMayHaveMutated = false
				primitiveConflict = true
				return err
			}
			return fmt.Errorf("%w: unpublish Home Skill %q: %w", sandbox.ErrOutcomeUnknown, request.Name, err)
		}
		// Inspecting managed shape alone cannot distinguish absence from an
		// ordinary replacement. Stat proves the direct entry is absent.
		_, err := filesystem.Stat(ctx, path.Join(sandbox.PathWorkspace, request.Name))
		if err == nil {
			err = errors.New("direct entry exists after unpublication")
			return fmt.Errorf("%w: verify unlinked Home Skill %q: %w", sandbox.ErrOutcomeUnknown, request.Name, err)
		}
		if errors.Is(err, sandbox.ErrOutcomeUnknown) {
			return err
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%w: verify unlinked Home Skill %q: %w", sandbox.ErrOutcomeUnknown, request.Name, err)
	})
	if primitiveConflict {
		return fmt.Errorf("%w: Home Skill %q does not match expected digest", ErrHomeSkillConflict, request.Name)
	}
	if errors.Is(err, sandbox.ErrOutcomeUnknown) {
		return err
	}
	if err != nil {
		if primitiveMayHaveMutated {
			return fmt.Errorf("%w: finalize Home Skill unpublication: %w", sandbox.ErrOutcomeUnknown, err)
		}
		return err
	}
	return nil
}

func snapshotHomeSkillRequest(request HomeSkillPublishRequest) (skillTree, error) {
	if request.Root == nil {
		return skillTree{}, errors.New("skills: Home Skill root is required")
	}
	if err := skillNameValidationError(request.Name, request.Name); err != nil {
		return skillTree{}, err
	}
	if request.ExpectedDigest != "" && !validHomeSkillDigest(request.ExpectedDigest) {
		return skillTree{}, errors.New("skills: expected digest must be empty or a lowercase SHA-256 digest")
	}
	metadata, err := snapshotHomeSkillMetadata(request.Metadata)
	if err != nil {
		return skillTree{}, err
	}
	files := make([]skillTreeEntry, len(request.Files))
	for i, file := range request.Files {
		files[i] = skillTreeEntry{Path: file.Path, Content: append([]byte(nil), file.Content...), Mode: file.Mode}
	}
	return skillTree{Metadata: metadata, Files: files}, nil
}

func snapshotHomeSkillUnpublishRequest(request HomeSkillUnpublishRequest) error {
	if request.Root == nil {
		return errors.New("skills: Home Skill root is required")
	}
	if err := skillNameValidationError(request.Name, request.Name); err != nil {
		return err
	}
	if !validHomeSkillDigest(request.ExpectedDigest) {
		return errors.New("skills: expected digest must be a lowercase SHA-256 digest")
	}
	return nil
}

// validateHomeSkillPublicationBounds applies the complete managed-revision
// ceiling before Home access. It includes Stella's metadata control file,
// which callers cannot name but which publication always writes.
func validateHomeSkillPublicationBounds(tree skillTree) error {
	if err := validateSkillTreeFiles(tree.Files); err != nil {
		return err
	}
	metadata, err := encodeSkillMetadataEnvelope(tree.Metadata)
	if err != nil {
		return err
	}
	if len(tree.Files)+1 > maxManagedTreeEntries {
		return errors.New("skills: Home Skill tree exceeds entry limit")
	}
	total := int64(len(metadata))
	if total > maxManagedFileBytes {
		return errors.New("skills: Home Skill metadata exceeds file limit")
	}
	for _, file := range tree.Files {
		if file.Path == MainFile && file.Mode != 0o644 {
			return errors.New("skills: SKILL.md mode must be exactly 0644")
		}
		if strings.Count(file.Path, "/")+1 > maxManagedTreeDepth {
			return fmt.Errorf("skills: Home Skill file %q exceeds directory depth", file.Path)
		}
		length := int64(len(file.Content))
		if length > maxManagedFileBytes {
			return fmt.Errorf("skills: Home Skill file %q exceeds file limit", file.Path)
		}
		if total > maxManagedTreeBytes-length {
			return errors.New("skills: Home Skill tree exceeds content limit")
		}
		total += length
	}
	if total > maxManagedTreeBytes {
		return errors.New("skills: Home Skill tree exceeds content limit")
	}
	return nil
}

func snapshotHomeSkillMetadata(metadata HomeSkillMetadata) (skillMetadataEnvelope, error) {
	encoded, err := canonicalJSON(metadata.Metadata)
	if err != nil {
		return skillMetadataEnvelope{}, fmt.Errorf("skills: invalid Home Skill metadata: %w", err)
	}
	value, err := decodeStrictJSON(encoded)
	if err != nil {
		return skillMetadataEnvelope{}, fmt.Errorf("skills: copy Home Skill metadata: %w", err)
	}
	copied, ok := value.(map[string]any)
	if !ok {
		return skillMetadataEnvelope{}, errors.New("skills: Home Skill metadata must be an object")
	}
	createdBy, ok := copied["created_by"].(string)
	if !ok || strings.TrimSpace(createdBy) == "" {
		return skillMetadataEnvelope{}, errors.New("skills: Home Skill metadata.created_by is required")
	}
	envelope := skillMetadataEnvelope{Status: metadata.Status, DisableModelInvocation: metadata.DisableModelInvocation, Metadata: copied, CreatedAt: metadata.CreatedAt, UpdatedAt: metadata.UpdatedAt, LegacyLifecycleVersion: metadata.LegacyLifecycleVersion}
	if err := validateSkillMetadataEnvelope(envelope); err != nil {
		return skillMetadataEnvelope{}, fmt.Errorf("skills: invalid Home Skill metadata: %w", err)
	}
	return envelope, nil
}

func checkHomeSkillExpected(ctx context.Context, filesystem sandbox.Filesystem, entry, name, expected string, target sandbox.ManagedSkillTarget) error {
	if expected != "" {
		if !target.Managed || target.Digest != expected {
			return fmt.Errorf("%w: Home Skill %q does not match expected digest", ErrHomeSkillConflict, name)
		}
		return nil
	}
	if target.Managed {
		return fmt.Errorf("%w: Home Skill %q already exists", ErrHomeSkillConflict, name)
	}
	_, err := filesystem.Stat(ctx, entry)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("skills: stat Home Skill %q: %w", name, err)
	}
	return fmt.Errorf("%w: Home Skill %q has an ordinary occupant", ErrHomeSkillConflict, name)
}

func skillTreePublication(tree skillTree) sandbox.ManagedSkillPublication {
	metadata, _ := encodeSkillMetadataEnvelope(tree.Metadata) // digest validated it before Home access.
	files := make([]sandbox.ManagedSkillTreeEntry, 0, len(tree.Files)+1)
	for _, file := range tree.Files {
		files = append(files, sandbox.ManagedSkillTreeEntry{Path: file.Path, Mode: file.Mode, Length: int64(len(file.Content)), Open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(file.Content)), nil
		}})
	}
	files = append(files, sandbox.ManagedSkillTreeEntry{Path: skillMetadataFile, Mode: 0o644, Length: int64(len(metadata)), Open: func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(metadata)), nil
	}})
	return sandbox.ManagedSkillPublication{Files: files}
}

func validHomeSkillDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func homeSkillPublishStripe(name string) int {
	hash := uint32(2166136261)
	for i := 0; i < len(name); i++ {
		hash ^= uint32(name[i])
		hash *= 16777619
	}
	return int(hash % homeSkillPublishStripeCount)
}
