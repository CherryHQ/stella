package skill

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
)

// ManagementAccess is the narrow Skill PEP port shared by HTTP and Stella
// management tools. Scope and owner identity are resolved from Authority, never
// supplied by a model argument.
type ManagementAccess interface {
	ManageScope(context.Context, authz.Authority, string, string) (userID, agentID string, err error)
	ManageByID(context.Context, authz.Authority, string, authz.Action) (Skill, error)
}

// PackageSkillCopyReader is the authority-bound plugin read seam used by
// CopyPackageSkill. It accepts only durable package identity, digest, and
// declared Skill name; it never accepts a host path.
type PackageSkillCopyReader interface {
	ReadPackageSkill(context.Context, authz.Authority, string, string, string) (PackageSkillRevision, error)
}

// WorkerAccess is the narrow PEP used by Reflect's fixed user_agent worker.
type WorkerAccess interface {
	AuthorizeWorkerWrite(context.Context, string, string, string, bool) error
}

// ManagementStore is the managed-Skill persistence and Home lifecycle surface.
type ManagementStore interface {
	IdentityReader
	CreateManagedSkill(context.Context, Skill, map[string]string) (SkillSnapshot, error)
	CreateManagedSkillWithFiles(context.Context, Skill, map[string]ManagedSkillFile) (SkillSnapshot, error)
	UpdateManagedSkill(context.Context, ManagedSkillUpdate) (SkillSnapshot, error)
	DeleteManagedSkill(context.Context, ManagedSkillDelete) error
}

// Management owns the application-level CRUD orchestration for managed Skills.
// Install and multipart upload remain HTTP-only because they accept external or
// unbounded sources; the model path accepts one bounded sandbox content_path.
type Management struct {
	store         ManagementStore
	access        ManagementAccess
	packageReader PackageSkillCopyReader
}

type ManagementOption func(*Management)

func WithPackageSkillReader(reader PackageSkillCopyReader) ManagementOption {
	return func(management *Management) { management.packageReader = reader }
}

func NewManagement(store ManagementStore, access ManagementAccess, options ...ManagementOption) *Management {
	management := &Management{store: store, access: access}
	for _, option := range options {
		if option != nil {
			option(management)
		}
	}
	return management
}

func (m *Management) List(ctx context.Context, authority authz.Authority, scope, targetAgentID string) ([]Skill, error) {
	userID, agentID, err := m.manageScope(ctx, authority, scope, targetAgentID)
	if err != nil {
		return nil, err
	}
	// Listing is a catalog operation. Its identity rows already carry the
	// current digest needed for CAS; opening every Home revision here would make
	// a model-visible metadata read unbounded in both I/O and returned content.
	return m.store.ListIdentityByScope(ctx, scope, userID, agentID)
}

func (m *Management) Create(ctx context.Context, authority authz.Authority, in ManagedCreate) (SkillSnapshot, error) {
	userID, agentID, err := m.manageScope(ctx, authority, in.Scope, in.TargetAgentID)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if in.Name == "" {
		return SkillSnapshot{}, fmt.Errorf("skill name is required")
	}
	if in.Files == nil || in.Files[MainFile] == "" {
		return SkillSnapshot{}, fmt.Errorf("files must include %s", MainFile)
	}
	return m.store.CreateManagedSkill(ctx, Skill{
		Scope: in.Scope, UserID: userID, AgentID: agentID, Name: in.Name,
		Description: in.Description, DisableModelInvocation: in.DisableModelInvocation, Metadata: in.Metadata,
		Status: SkillStatusActive,
	}, in.Files)
}

func (m *Management) Get(ctx context.Context, authority authz.Authority, id string) (ManagedRevision, error) {
	skill, err := m.manageByID(ctx, authority, id, authz.ActionRead)
	if err != nil {
		return ManagedRevision{}, err
	}
	return m.store.LoadCurrentRevision(ctx, skill)
}

func (m *Management) Update(ctx context.Context, authority authz.Authority, in ManagedUpdate) (SkillSnapshot, error) {
	identity, err := m.manageByID(ctx, authority, in.ID, authz.ActionWrite)
	if err != nil {
		return SkillSnapshot{}, err
	}
	current, err := m.store.LoadCurrentRevision(ctx, identity)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if err := checkExpectedDigest(in.ExpectedVersion, current.Skill.ContentDigest); err != nil {
		return SkillSnapshot{}, err
	}
	if current.Skill.Status == SkillStatusDeprecated {
		return SkillSnapshot{}, ErrSkillNotMutable
	}
	if in.Name != "" && in.Name != current.Skill.Name {
		return SkillSnapshot{}, fmt.Errorf("SKILL.md name %q does not match managed Skill name %q", in.Name, current.Skill.Name)
	}
	patch := in.Patch
	if in.Version != nil {
		metadata, err := mergeMetadataVersion(current.Skill.Metadata, *in.Version)
		if err != nil {
			return SkillSnapshot{}, fmt.Errorf("update skill version metadata: %w", err)
		}
		patch.Metadata = metadata
	}
	deleteFiles := []string(nil)
	if in.ReplaceFiles {
		for filename := range current.Files {
			if _, retained := in.Files[filename]; !retained {
				deleteFiles = append(deleteFiles, filename)
			}
		}
	}
	deleteFiles = append(deleteFiles, in.DeleteFiles...)
	return m.store.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: current.Skill.ID, UserID: current.Skill.UserID, AgentID: current.Skill.AgentID, Scope: current.Skill.Scope,
		Patch: patch, Files: in.Files, DeleteFiles: deleteFiles, ConvertToManual: in.ConvertToManual,
		ExpectedDigest: in.ExpectedVersion,
	})
}

func (m *Management) Delete(ctx context.Context, authority authz.Authority, id, expectedVersion string) error {
	identity, err := m.manageByID(ctx, authority, id, authz.ActionDelete)
	if err != nil {
		return err
	}
	current, err := m.store.LoadCurrentRevision(ctx, identity)
	if err != nil {
		return err
	}
	if err := checkExpectedDigest(expectedVersion, current.Skill.ContentDigest); err != nil {
		return err
	}
	return m.store.DeleteManagedSkill(ctx, ManagedSkillDelete{
		ID: current.Skill.ID, UserID: current.Skill.UserID, AgentID: current.Skill.AgentID, Scope: current.Skill.Scope,
		ExpectedDigest: expectedVersion,
	})
}

// Install authorizes the destination before fetching any remote or local source.
func (m *Management) Install(ctx context.Context, authority authz.Authority, in ManagedInstall) (SkillSnapshot, error) {
	userID, agentID, err := m.manageScope(ctx, authority, in.Scope, in.TargetAgentID)
	if err != nil {
		return SkillSnapshot{}, err
	}
	return InstallToStore(ctx, m.store, in.Source, in.Scope, userID, agentID)
}

// Upgrade authorizes the durable identity before fetching its recorded source.
func (m *Management) Upgrade(ctx context.Context, authority authz.Authority, in ManagedUpgrade) (UpgradeResult, error) {
	identity, err := m.manageByID(ctx, authority, in.ID, authz.ActionWrite)
	if err != nil {
		return UpgradeResult{}, err
	}
	current, err := m.store.LoadCurrentRevision(ctx, identity)
	if err != nil {
		return UpgradeResult{}, err
	}
	if err := checkExpectedDigest(in.ExpectedVersion, current.Skill.ContentDigest); err != nil {
		return UpgradeResult{}, err
	}
	return UpgradeInStore(ctx, m.store, current.Skill, in.ExpectedVersion, current.Skill.Metadata)
}

// DeleteFile is the file-level mutation entry point. It shares the same
// identity authorization and digest CAS as ordinary updates and deletes.
func (m *Management) DeleteFile(ctx context.Context, authority authz.Authority, id, path, expectedVersion string) (SkillSnapshot, error) {
	return m.Update(ctx, authority, ManagedUpdate{
		ID:              id,
		ExpectedVersion: expectedVersion,
		DeleteFiles:     []string{path},
	})
}

func (m *Management) manageScope(ctx context.Context, authority authz.Authority, scope, agentID string) (string, string, error) {
	if m == nil || m.store == nil || m.access == nil {
		return "", "", ErrManagedSkillsUnavailable
	}
	return m.access.ManageScope(ctx, authority, scope, agentID)
}

func (m *Management) manageByID(ctx context.Context, authority authz.Authority, id string, action authz.Action) (Skill, error) {
	if m == nil || m.store == nil || m.access == nil {
		return Skill{}, ErrManagedSkillsUnavailable
	}
	return m.access.ManageByID(ctx, authority, id, action)
}

type ManagedCreate struct {
	Scope                  string
	TargetAgentID          string
	Name                   string
	Description            string
	DisableModelInvocation bool
	Metadata               json.RawMessage
	Files                  map[string]string
}

type ManagedUpdate struct {
	ID              string
	ExpectedVersion string
	Name            string
	Patch           UpdatePatch
	Version         *string
	Files           map[string]string
	DeleteFiles     []string
	ReplaceFiles    bool
	ConvertToManual bool
}

// ManagedInstall describes a source-backed install. The source is fetched only
// after Management has authorized the target scope.
type ManagedInstall struct {
	Source        string
	Scope         string
	TargetAgentID string
}

// ManagedUpgrade describes an in-place source-backed upgrade.
type ManagedUpgrade struct {
	ID              string
	ExpectedVersion string
}

// reflectStore is the existing constrained read/write port used by Reflect.
// Management owns the write side; exact reads remain on the store so persisted
// old-revision references are not incorrectly re-authorized as current rows.
type reflectStore interface {
	ListActiveReflectOwnedUserAgentSkills(context.Context, string, string) ([]Skill, error)
	LoadExactRevision(context.Context, Skill, string) (ManagedRevision, error)
	CreateReflectOwnedUserAgentSkill(context.Context, ReflectSkillCreate) (Skill, error)
	PatchReflectOwnedUserAgentSkill(context.Context, ReflectSkillPatch) (Skill, error)
	DeleteReflectOwnedUserAgentSkill(context.Context, ReflectSkillDelete) (Skill, error)
}

// ReflectWorker keeps Reflect's existing exact-read port while routing every
// durable write through Management's fixed worker authorization.
type ReflectWorker struct {
	store  reflectStore
	access WorkerAccess
}

func (m *Management) NewReflectWorker() *ReflectWorker {
	if m == nil {
		return nil
	}
	store, _ := m.store.(reflectStore)
	access, _ := m.access.(WorkerAccess)
	return &ReflectWorker{store: store, access: access}
}

func (w *ReflectWorker) ListActiveReflectOwnedUserAgentSkills(ctx context.Context, userID, agentID string) ([]Skill, error) {
	if w == nil || w.store == nil {
		return nil, ErrManagedSkillsUnavailable
	}
	return w.store.ListActiveReflectOwnedUserAgentSkills(ctx, userID, agentID)
}

func (w *ReflectWorker) LoadExactRevision(ctx context.Context, identity Skill, digest string) (ManagedRevision, error) {
	if w == nil || w.store == nil {
		return ManagedRevision{}, ErrManagedSkillsUnavailable
	}
	return w.store.LoadExactRevision(ctx, identity, digest)
}

func (w *ReflectWorker) CreateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillCreate) (Skill, error) {
	if w == nil || w.store == nil || w.access == nil {
		return Skill{}, ErrManagedSkillsUnavailable
	}
	if err := w.access.AuthorizeWorkerWrite(ctx, in.UserID, in.AgentID, "", true); err != nil {
		return Skill{}, err
	}
	return w.store.CreateReflectOwnedUserAgentSkill(ctx, in)
}

func (w *ReflectWorker) PatchReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillPatch) (Skill, error) {
	if w == nil || w.store == nil || w.access == nil {
		return Skill{}, ErrManagedSkillsUnavailable
	}
	if err := w.access.AuthorizeWorkerWrite(ctx, in.UserID, in.AgentID, in.ID, false); err != nil {
		return Skill{}, err
	}
	return w.store.PatchReflectOwnedUserAgentSkill(ctx, in)
}

func (w *ReflectWorker) DeleteReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillDelete) (Skill, error) {
	if w == nil || w.store == nil || w.access == nil {
		return Skill{}, ErrManagedSkillsUnavailable
	}
	if err := w.access.AuthorizeWorkerWrite(ctx, in.UserID, in.AgentID, in.ID, false); err != nil {
		return Skill{}, err
	}
	return w.store.DeleteReflectOwnedUserAgentSkill(ctx, in)
}

func checkExpectedDigest(expected, current string) error {
	if expected == "" {
		return ErrSkillDigestRequired
	}
	if expected != current {
		return ErrSkillDigestConflict
	}
	return nil
}

// mergeMetadataVersion changes only the installed-version marker, preserving
// source and provenance metadata. An explicit empty version removes the marker.
func mergeMetadataVersion(metadata json.RawMessage, version string) (json.RawMessage, error) {
	values := map[string]any{}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &values); err != nil {
			return nil, err
		}
	}
	if version == "" {
		delete(values, "version")
	} else {
		values["version"] = version
	}
	return json.Marshal(values)
}
