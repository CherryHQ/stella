package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

const sharedSkillPublishStripeCount = 257

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

// SharedSkillPublishRequest names one shared root entry. ExpectedDigest is an
// optimistic concurrency token: empty creates only an absent direct entry;
// otherwise it must be the exact current managed digest.
type SharedSkillPublishRequest struct {
	Root           home.Key
	Name           string
	ExpectedDigest string
	Metadata       SharedSkillMetadata
	Files          []SharedSkillFile
}

// SharedSkillPublisher publishes complete, digest-pinned revisions to system
// and system_agent Homes. Stripes are process-local for the Phase-1 single
// replica ceiling; replace them with Phase-4 PG advisory locking when writers
// can run in more than one replica.
type SharedSkillPublisher struct {
	homes   SharedSkillFilesystemAccess
	stripes [sharedSkillPublishStripeCount]chan struct{}
}

func NewSharedSkillPublisher(homes SharedSkillFilesystemAccess) (*SharedSkillPublisher, error) {
	if homes == nil {
		return nil, errors.New("skills: shared Skill filesystem access is required")
	}
	p := &SharedSkillPublisher{homes: homes}
	for i := range p.stripes {
		p.stripes[i] = make(chan struct{}, 1)
		p.stripes[i] <- struct{}{}
	}
	return p, nil
}

// Publish validates and snapshots the complete caller-owned request before it
// opens Home bytes, then publishes it at the fixed workspace root.
func (p *SharedSkillPublisher) Publish(ctx context.Context, request SharedSkillPublishRequest) (string, error) {
	if p == nil || p.homes == nil {
		return "", errors.New("skills: shared Skill publisher is unavailable")
	}
	tree, err := snapshotSharedSkillRequest(request)
	if err != nil {
		return "", err
	}
	digest, err := digestSkillTree(tree)
	if err != nil {
		return "", fmt.Errorf("skills: validate shared Skill tree: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	stripe := p.stripes[sharedSkillPublishStripe(request.Root, request.Name)]
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-stripe:
	}
	defer func() { stripe <- struct{}{} }()

	err = p.homes.UseSharedSkillFilesystem(ctx, request.Root, func(filesystem sandbox.Filesystem) error {
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
			// A malformed or foreign symlink is an ordinary occupant for this
			// authority. It is never a candidate for replacement.
			return fmt.Errorf("%w: inspect direct entry %q: %w", ErrSharedSkillConflict, request.Name, err)
		}
		if err := checkSharedSkillExpected(ctx, filesystem, entry, request.Name, request.ExpectedDigest, target); err != nil {
			return err
		}

		if err := publisher.PublishManagedSkill(ctx, sandbox.PathWorkspace, request.Name, digest, skillTreePublication(tree)); err != nil {
			// Once the publisher has been invoked, it may have selected a
			// revision even when it reports an ordinary failure. A retry or
			// clean conflict would therefore lie about the observable outcome.
			return fmt.Errorf("%w: publish shared Skill %q: %w", sandbox.ErrOutcomeUnknown, request.Name, err)
		}
		selected, err := inspector.InspectManagedSkillTarget(ctx, entry)
		if err != nil {
			return fmt.Errorf("%w: verify selected shared Skill %q: %w", sandbox.ErrOutcomeUnknown, request.Name, err)
		}
		if !selected.Managed || selected.Digest != digest {
			return fmt.Errorf("%w: selected digest %q, want %q", sandbox.ErrOutcomeUnknown, selected.Digest, digest)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return digest, nil
}

func snapshotSharedSkillRequest(request SharedSkillPublishRequest) (skillTree, error) {
	if err := validateSharedSkillRoot(request.Root); err != nil {
		return skillTree{}, err
	}
	if err := skillNameValidationError(request.Name, request.Name); err != nil {
		return skillTree{}, err
	}
	if request.ExpectedDigest != "" && !validSharedSkillDigest(request.ExpectedDigest) {
		return skillTree{}, errors.New("skills: expected digest must be empty or a lowercase SHA-256 digest")
	}
	metadata, err := snapshotSharedSkillMetadata(request.Metadata)
	if err != nil {
		return skillTree{}, err
	}
	files := make([]skillTreeEntry, len(request.Files))
	for i, file := range request.Files {
		files[i] = skillTreeEntry{Path: file.Path, Content: append([]byte(nil), file.Content...), Mode: file.Mode}
	}
	return skillTree{Metadata: metadata, Files: files}, nil
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

func snapshotSharedSkillMetadata(metadata SharedSkillMetadata) (skillMetadataEnvelope, error) {
	encoded, err := canonicalJSON(metadata.Metadata)
	if err != nil {
		return skillMetadataEnvelope{}, fmt.Errorf("skills: invalid shared Skill metadata: %w", err)
	}
	value, err := decodeStrictJSON(encoded)
	if err != nil {
		return skillMetadataEnvelope{}, fmt.Errorf("skills: copy shared Skill metadata: %w", err)
	}
	copied, ok := value.(map[string]any)
	if !ok {
		return skillMetadataEnvelope{}, errors.New("skills: shared Skill metadata must be an object")
	}
	createdBy, ok := copied["created_by"].(string)
	if !ok || strings.TrimSpace(createdBy) == "" {
		return skillMetadataEnvelope{}, errors.New("skills: shared Skill metadata.created_by is required")
	}
	envelope := skillMetadataEnvelope{Status: metadata.Status, DisableModelInvocation: metadata.DisableModelInvocation, Metadata: copied, CreatedAt: metadata.CreatedAt, UpdatedAt: metadata.UpdatedAt, LegacyLifecycleVersion: metadata.LegacyLifecycleVersion}
	if err := validateSkillMetadataEnvelope(envelope); err != nil {
		return skillMetadataEnvelope{}, fmt.Errorf("skills: invalid shared Skill metadata: %w", err)
	}
	return envelope, nil
}

func checkSharedSkillExpected(ctx context.Context, filesystem sandbox.Filesystem, entry, name, expected string, target sandbox.ManagedSkillTarget) error {
	if expected != "" {
		if !target.Managed || target.Digest != expected {
			return fmt.Errorf("%w: shared Skill %q does not match expected digest", ErrSharedSkillConflict, name)
		}
		return nil
	}
	if target.Managed {
		return fmt.Errorf("%w: shared Skill %q already exists", ErrSharedSkillConflict, name)
	}
	_, err := filesystem.Stat(ctx, entry)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("skills: stat shared Skill %q: %w", name, err)
	}
	return fmt.Errorf("%w: shared Skill %q has an ordinary occupant", ErrSharedSkillConflict, name)
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

func validSharedSkillDigest(digest string) bool {
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

func sharedSkillPublishStripe(key home.Key, name string) int {
	value := string(key.Kind) + "\x00" + key.AgentID + "\x00" + name
	hash := uint32(2166136261)
	for i := 0; i < len(value); i++ {
		hash ^= uint32(value[i])
		hash *= 16777619
	}
	return int(hash % sharedSkillPublishStripeCount)
}
