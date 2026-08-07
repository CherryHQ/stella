package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

var ErrSharedSkillConflict = errors.New("skills: shared Skill publication conflict")

// SharedSkillFilesystemAccess is the sole Home capability needed by the shared
// publisher. It deliberately provides a callback over a fixed sandbox root,
// rather than a Home record, attachment, locator, or host path.
type SharedSkillFilesystemAccess interface {
	UseSharedSkillFilesystem(context.Context, home.Key, func(sandbox.Filesystem) error) error
}

// SharedSkillMetadata is the canonical filesystem metadata owned by Stella.
// CreatedBy must live in Metadata so it remains part of the revision digest.
type SharedSkillMetadata struct {
	Status                 string
	DisableModelInvocation bool
	Metadata               map[string]any
	CreatedAt              time.Time
	UpdatedAt              time.Time
	LegacyLifecycleVersion int64
}

// SharedSkillFile is one opaque regular file in a complete Skill tree.
type SharedSkillFile struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

// SharedSkillPublishRequest preserves the original shared-only contract.
// ExpectedDigest is an optimistic concurrency token: empty creates only an
// absent direct entry; otherwise it must be the exact current managed digest.
type SharedSkillPublishRequest struct {
	Root           home.Key
	Name           string
	ExpectedDigest string
	Metadata       SharedSkillMetadata
	Files          []SharedSkillFile
}

// SharedSkillPublisher preserves the shared system/system_agent publication
// API. It adapts that strict legacy contract to the typed Home publisher; user
// and user_agent roots are deliberately rejected before any Home access.
type SharedSkillPublisher struct {
	homes             SharedSkillFilesystemAccess
	core              managedSkillPublicationCore
	revisionTelemetry *RevisionTelemetry
	catalogRoot       func(context.Context, home.Key) (FilesystemCatalogRoot, error)
}

func NewSharedSkillPublisher(homes SharedSkillFilesystemAccess) (*SharedSkillPublisher, error) {
	if homes == nil {
		return nil, errors.New("skills: shared Skill filesystem access is required")
	}
	return &SharedSkillPublisher{homes: homes, core: newManagedSkillPublicationCore()}, nil
}

// NewSharedSkillPublisherWithRevisionTelemetry adds best-effort retained
// revision observation after verified publications. The resolver is supplied by
// the authority wiring because only it can bind a Home to an opaque catalog root.
func NewSharedSkillPublisherWithRevisionTelemetry(homes SharedSkillFilesystemAccess, telemetry *RevisionTelemetry, catalogRoot func(context.Context, home.Key) (FilesystemCatalogRoot, error)) (*SharedSkillPublisher, error) {
	publisher, err := NewSharedSkillPublisher(homes)
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
// opens Home bytes, then publishes it at the fixed shared workspace root.
func (p *SharedSkillPublisher) Publish(ctx context.Context, request SharedSkillPublishRequest) (string, error) {
	if p == nil || p.homes == nil {
		return "", errors.New("skills: shared Skill publisher is unavailable")
	}
	root, err := sharedSkillCatalogRoot(request.Root)
	if err != nil {
		return "", err
	}
	homeRequest := sharedSkillHomeRequest(root, request)
	digest, err := p.core.publish(ctx, homeRequest, func(ctx context.Context, _ *home.SkillRoot, use func(sandbox.Filesystem) error) error {
		return p.homes.UseSharedSkillFilesystem(ctx, request.Root, use)
	}, func(ctx context.Context, _ *home.SkillRoot, filesystem sandbox.Filesystem) {
		p.observeVerifiedPublication(ctx, request.Root, filesystem)
	})
	if errors.Is(err, ErrHomeSkillConflict) {
		return "", fmt.Errorf("%w: %w", ErrSharedSkillConflict, err)
	}
	if err != nil {
		return "", err
	}
	return digest, nil
}

func sharedSkillHomeRequest(root *home.SkillRoot, request SharedSkillPublishRequest) HomeSkillPublishRequest {
	files := make([]HomeSkillFile, len(request.Files))
	for i, file := range request.Files {
		files[i] = HomeSkillFile(file)
	}
	return HomeSkillPublishRequest{
		Root: root, Name: request.Name, ExpectedDigest: request.ExpectedDigest,
		Metadata: HomeSkillMetadata(request.Metadata), Files: files,
	}
}

func sharedSkillCatalogRoot(key home.Key) (*home.SkillRoot, error) {
	if err := validateSharedSkillRoot(key); err != nil {
		return nil, err
	}
	switch key.Kind {
	case home.SystemSkillRoot:
		return home.SystemSkillCatalog(), nil
	case home.SystemAgentSkillRoot:
		return home.SystemAgentSkillCatalog(key.AgentID)
	default:
		panic("validated shared Skill root has unsupported kind")
	}
}

func (p *SharedSkillPublisher) observeVerifiedPublication(ctx context.Context, key home.Key, filesystem sandbox.Filesystem) {
	if p.revisionTelemetry == nil {
		return
	}
	root, err := p.catalogRoot(ctx, key)
	if err != nil {
		slog.Warn("shared Skill revision telemetry failed after publication", "reason", "root_unavailable")
		return
	}
	// Observe logs path-safe collection failures itself. A verified publication
	// remains successful regardless.
	_ = p.revisionTelemetry.Observe(ctx, filesystem, root)
}

func validateSharedSkillRoot(key home.Key) error {
	if err := key.Validate(); err != nil {
		return fmt.Errorf("skills: invalid shared Skill root: %w", err)
	}
	switch key.Kind {
	case home.SystemSkillRoot:
		if key != home.SystemSkills() {
			return errors.New("skills: system Skill root identity is invalid")
		}
	case home.SystemAgentSkillRoot:
		if key != home.SystemAgentSkills(key.AgentID) {
			return errors.New("skills: system Agent Skill root identity is invalid")
		}
	default:
		return errors.New("skills: shared Skill root must be SystemSkills or SystemAgentSkills")
	}
	return nil
}
