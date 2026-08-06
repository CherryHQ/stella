package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"strings"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

const reflectLastChangeMetadataKey = "reflect_last_change"

// HomeReflectStore is Reflect's prospective Home-authoritative write boundary.
// It combines one exact Home current-state authority with logical PostgreSQL
// usage telemetry, but deliberately is not the legacy Store interface: wiring
// that still uses PG Skill rows cannot accidentally use this implementation.
type HomeReflectStore struct {
	home  *HomeStore
	usage *HomeSkillUsageStore
}

func NewHomeReflectStore(home *HomeStore, usage *HomeSkillUsageStore) (*HomeReflectStore, error) {
	if home == nil || usage == nil {
		return nil, errors.New("skills: Home Reflect store requires Home and usage stores")
	}
	if home.catalog == nil || home.manager == nil || home.manager.catalog != home.catalog {
		return nil, errors.New("skills: Home Reflect store requires one unsplit Home authority")
	}
	return &HomeReflectStore{home: home, usage: usage}, nil
}

// CreateReflectOwnedUserAgentSkill publishes one complete active user_agent
// revision before it records its derived usage fact. A telemetry failure after
// publication is necessarily ambiguous and must not be retried transparently.
func (s *HomeReflectStore) CreateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillCreate) (Skill, error) {
	request, metadata, err := snapshotHomeReflectCreate(in)
	if err != nil {
		return Skill{}, err
	}
	snapshot, err := s.home.manager.Create(ctx, request)
	if err == nil {
		if _, err := s.usage.InitializeReflectCreate(ctx, homeReflectUsageIdentity(snapshot.Skill)); err != nil {
			return Skill{}, reflectTelemetryOutcome("initialize Home Reflect usage", err)
		}
		return snapshot.Skill, nil
	}
	if !errors.Is(err, ErrHomeSkillConflict) {
		return Skill{}, err
	}
	retried, err := s.resolveCreateRetry(ctx, in, metadata)
	if err != nil {
		return Skill{}, err
	}
	if _, err := s.usage.InitializeReflectCreate(ctx, homeReflectUsageIdentity(retried.Skill)); err != nil {
		return Skill{}, reflectTelemetryOutcome("initialize Home Reflect usage", err)
	}
	return retried.Skill, nil
}

// PatchReflectOwnedUserAgentSkill replaces one complete Home revision under an
// immutable digest CAS. ExpectedVersion is intentionally ignored here: it is
// legacy PG lifecycle metadata, not Home current-state authority.
func (s *HomeReflectStore) PatchReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillPatch) (Skill, error) {
	if err := validateHomeReflectPatch(in); err != nil {
		return Skill{}, err
	}
	before, err := s.loadReflectSnapshot(ctx, in.ID, in.UserID, in.AgentID)
	if err != nil {
		return Skill{}, err
	}
	if before.ContentDigest != in.ExpectedDigest {
		return s.resolvePatchRetry(ctx, in, before)
	}
	request, err := s.homeReflectPatchRequest(before, in)
	if err != nil {
		return Skill{}, err
	}
	after, err := s.home.manager.Update(ctx, request)
	if errors.Is(err, ErrHomeSkillConflict) {
		// A writer can win after our snapshot but before the manager CAS. Only an
		// exact completed request is a legal retry; all other races stay closed.
		current, loadErr := s.loadReflectSnapshot(ctx, in.ID, in.UserID, in.AgentID)
		if loadErr != nil {
			return Skill{}, loadErr
		}
		return s.resolvePatchRetry(ctx, in, current)
	}
	if err != nil {
		return Skill{}, err
	}
	identity := homeReflectUsageIdentity(before.Skill)
	if _, err := s.usage.PatchReflectDigest(ctx, identity, after.Skill.ContentDigest); err != nil {
		return Skill{}, reflectTelemetryOutcome("patch Home Reflect usage", err)
	}
	return after.Skill, nil
}

// TouchReflectSkillRuntimeUse records a runtime observation at the caller's
// exact Home revision. It intentionally never opens Home to invent a digest.
func (s *HomeReflectStore) TouchReflectSkillRuntimeUse(ctx context.Context, id, userID, agentID, contentDigest string) error {
	identity, err := newHomeReflectUsageIdentity(id, userID, agentID, contentDigest)
	if err != nil {
		return err
	}
	return s.usage.TouchReflectRuntimeUse(ctx, identity)
}

// DeleteReflectOwnedUserAgentSkill first proves the exact active Home state,
// then atomically consumes the usage/activity curator decision. From that
// point forward the telemetry has changed, so any unpublish/callback/close
// failure is outcome-unknown; there is deliberately no rollback or retry.
func (s *HomeReflectStore) DeleteReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillDelete) (Skill, error) {
	if err := validateHomeReflectDelete(in); err != nil {
		return Skill{}, err
	}
	before, err := s.loadReflectSnapshot(ctx, in.ID, in.UserID, in.AgentID)
	if err != nil {
		return Skill{}, err
	}
	if before.ContentDigest != in.ExpectedDigest {
		return Skill{}, ErrHomeSkillConflict
	}
	identity := homeReflectUsageIdentity(before.Skill)
	if err := s.usage.DeleteForCurator(ctx, identity, in.ExpectedUsageLastUsedAt, in.ExpectedPairLatestActivityAt); err != nil {
		return Skill{}, err
	}
	if err := s.home.manager.Delete(ctx, before.Skill.ID, before.ContentDigest); err != nil {
		return Skill{}, reflectTelemetryOutcome("unpublish Home Reflect Skill after usage deletion", err)
	}
	return before.Skill, nil
}

func snapshotHomeReflectCreate(in ReflectSkillCreate) (HomeSkillCreateRequest, map[string]any, error) {
	if err := validateReflectSkillName(in.Name); err != nil {
		return HomeSkillCreateRequest{}, nil, err
	}
	if in.UserID == "" || in.AgentID == "" {
		return HomeSkillCreateRequest{}, nil, errors.New("skills: user_id and agent_id are required")
	}
	if in.MainFileContent == "" {
		return HomeSkillCreateRequest{}, nil, errors.New("skills: SKILL.md content is required")
	}
	description := strings.TrimSpace(in.Description)
	if description == "" {
		return HomeSkillCreateRequest{}, nil, errors.New("skills: description is required")
	}
	metadata, err := mergeHomeReflectMetadata(nil, in.Metadata, in.ChangelogMetadata)
	if err != nil {
		return HomeSkillCreateRequest{}, nil, err
	}
	if err := validateHomeCatalogMetadataMap(metadata); err != nil {
		return HomeSkillCreateRequest{}, nil, err
	}
	return HomeSkillCreateRequest{
		Scope: "user_agent", UserID: in.UserID, AgentID: in.AgentID, Name: in.Name,
		Description: description, Metadata: metadata,
		Files: []HomeSkillFileInput{{Path: MainFile, Content: []byte(in.MainFileContent), Mode: regularSkillMode()}},
	}, metadata, nil
}

func validateHomeReflectPatch(in ReflectSkillPatch) error {
	if in.ID == "" || in.UserID == "" || in.AgentID == "" {
		return errors.New("skills: id, user_id, and agent_id are required")
	}
	scope, userID, agentID, name, err := decodeFilesystemSkillID(in.ID)
	if err != nil {
		return err
	}
	if scope != "user_agent" || userID != in.UserID || agentID != in.AgentID {
		return ErrSkillNotReflectOwned
	}
	if err := validateReflectSkillName(name); err != nil {
		return err
	}
	if !validHomeSkillDigest(in.ExpectedDigest) {
		return errors.New("skills: expected digest is required")
	}
	if in.Status != nil {
		return errors.New("skills: Reflect Home patch does not support status mutation")
	}
	_, err = mergeHomeReflectMetadata(map[string]any{}, in.Metadata, in.ChangelogMetadata)
	return err
}

func validateHomeReflectDelete(in ReflectSkillDelete) error {
	if in.ID == "" || in.UserID == "" || in.AgentID == "" {
		return errors.New("skills: id, user_id, and agent_id are required")
	}
	scope, userID, agentID, name, err := decodeFilesystemSkillID(in.ID)
	if err != nil {
		return err
	}
	if scope != "user_agent" || userID != in.UserID || agentID != in.AgentID {
		return ErrSkillNotReflectOwned
	}
	if err := validateReflectSkillName(name); err != nil {
		return err
	}
	if !validHomeSkillDigest(in.ExpectedDigest) {
		return errors.New("skills: expected digest is required")
	}
	if in.ExpectedUsageLastUsedAt.IsZero() {
		return errors.New("skills: expected_usage_last_used_at is required")
	}
	if in.ExpectedPairLatestActivityAt.IsZero() {
		return errors.New("skills: expected_pair_latest_activity_at is required")
	}
	return nil
}

func (s *HomeReflectStore) loadReflectSnapshot(ctx context.Context, id, userID, agentID string) (HomeManagedSkillSnapshot, error) {
	snapshot, err := s.home.catalog.LoadManagedSnapshot(ctx, id)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrSkillNotMutable) {
		return HomeManagedSkillSnapshot{}, ErrHomeSkillConflict
	}
	if err != nil {
		return HomeManagedSkillSnapshot{}, err
	}
	if err := validateHomeReflectSnapshot(snapshot, userID, agentID); err != nil {
		return HomeManagedSkillSnapshot{}, err
	}
	return snapshot, nil
}

func validateHomeReflectSnapshot(snapshot HomeManagedSkillSnapshot, userID, agentID string) error {
	if snapshot.Skill.Scope != "user_agent" || snapshot.Skill.UserID != userID || snapshot.Skill.AgentID != agentID || CreatedBy(snapshot.Skill) != ReflectSkillCreatedBy {
		return ErrSkillNotReflectOwned
	}
	if snapshot.Skill.Status != SkillStatusActive {
		return ErrHomeSkillConflict
	}
	return nil
}

func (s *HomeReflectStore) homeReflectPatchRequest(before HomeManagedSkillSnapshot, in ReflectSkillPatch) (HomeSkillUpdateRequest, error) {
	metadata, err := mergeHomeReflectMetadataFromJSON(before.Skill.Metadata, in.Metadata, in.ChangelogMetadata)
	if err != nil {
		return HomeSkillUpdateRequest{}, err
	}
	request := HomeSkillUpdateRequest{ID: before.Skill.ID, ExpectedDigest: before.ContentDigest, Metadata: &metadata}
	if in.Description != nil {
		description := strings.TrimSpace(*in.Description)
		if description == "" {
			return HomeSkillUpdateRequest{}, errors.New("skills: description is required")
		}
		request.Description = &description
	}
	if in.DisableModelInvocation != nil {
		disable := *in.DisableModelInvocation
		request.DisableModelInvocation = &disable
	}
	if in.MainFileContent != nil {
		request.FileUpserts = []HomeSkillFileInput{{Path: MainFile, Content: []byte(*in.MainFileContent), Mode: regularSkillMode()}}
	}
	return request, nil
}

func (s *HomeReflectStore) resolveCreateRetry(ctx context.Context, in ReflectSkillCreate, metadata map[string]any) (HomeManagedSkillSnapshot, error) {
	id, err := encodeFilesystemSkillID("user_agent", in.UserID, in.AgentID, in.Name)
	if err != nil {
		return HomeManagedSkillSnapshot{}, err
	}
	snapshot, err := s.loadReflectSnapshot(ctx, id, in.UserID, in.AgentID)
	if err != nil {
		return HomeManagedSkillSnapshot{}, ErrHomeSkillConflict
	}
	wantMain, err := rewriteSkillFrontmatter([]byte(in.MainFileContent), in.Name, strings.TrimSpace(in.Description))
	if err != nil || !homeReflectSnapshotMatches(snapshot, strings.TrimSpace(in.Description), false, metadata, []HomeSkillFile{{Path: MainFile, Content: wantMain, Mode: 0o644}}) {
		return HomeManagedSkillSnapshot{}, ErrHomeSkillConflict
	}
	return snapshot, nil
}

func (s *HomeReflectStore) resolvePatchRetry(ctx context.Context, in ReflectSkillPatch, current HomeManagedSkillSnapshot) (Skill, error) {
	expected, err := s.home.catalog.LoadManagedRevision(ctx, in.ID, in.ExpectedDigest)
	if errors.Is(err, fs.ErrNotExist) {
		return Skill{}, ErrHomeSkillConflict
	}
	if err != nil {
		return Skill{}, ErrHomeSkillConflict
	}
	if err := validateHomeReflectSnapshot(expected, in.UserID, in.AgentID); err != nil {
		return Skill{}, err
	}
	request, err := s.homeReflectPatchRequest(expected, in)
	if err != nil {
		return Skill{}, err
	}
	if !homeReflectSnapshotMatches(current, reflectPatchDescription(expected, request), reflectPatchDisable(expected, request), *request.Metadata, reflectPatchFiles(expected, request)) {
		return Skill{}, ErrHomeSkillConflict
	}
	identity, err := newHomeReflectUsageIdentity(in.ID, in.UserID, in.AgentID, in.ExpectedDigest)
	if err != nil {
		return Skill{}, err
	}
	if _, err := s.usage.PatchReflectDigest(ctx, identity, current.ContentDigest); err != nil {
		return Skill{}, reflectTelemetryOutcome("complete Home Reflect patch retry", err)
	}
	return current.Skill, nil
}

func mergeHomeReflectMetadata(existing map[string]any, rawEntity, rawProvenance json.RawMessage) (map[string]any, error) {
	base, err := copyHomeSkillMetadata(existing)
	if err != nil {
		return nil, err
	}
	if len(rawEntity) != 0 {
		entity, err := homeStoreMetadataMap(rawEntity)
		if err != nil {
			return nil, fmt.Errorf("skills: Reflect entity metadata: %w", err)
		}
		if _, collision := entity[reflectLastChangeMetadataKey]; collision {
			return nil, fmt.Errorf("skills: Reflect entity metadata cannot set %q", reflectLastChangeMetadataKey)
		}
		maps.Copy(base, entity)
	}
	provenance, err := homeReflectProvenance(rawProvenance)
	if err != nil {
		return nil, err
	}
	// Changelog data is audit history, not entity state. One canonical nested
	// object makes each retained revision self-contained without key collisions.
	base[reflectLastChangeMetadataKey] = provenance
	base[reflectSkillCreatedByKey] = ReflectSkillCreatedBy
	if err := validateHomeCatalogMetadataMap(base); err != nil {
		return nil, err
	}
	return base, nil
}

func mergeHomeReflectMetadataFromJSON(existing json.RawMessage, rawEntity, rawProvenance json.RawMessage) (map[string]any, error) {
	base, err := homeStoreMetadataMap(existing)
	if err != nil {
		return nil, fmt.Errorf("skills: decode Reflect Home metadata: %w", err)
	}
	return mergeHomeReflectMetadata(base, rawEntity, rawProvenance)
}

// homeReflectProvenance keeps ChangelogMetadata input compatible while making
// Home's retained immutable revision a strict, canonical and bounded audit
// record. The enclosing metadata validation applies the durable size ceiling.
func homeReflectProvenance(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	value, err := decodeStrictJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("skills: Reflect changelog metadata must be a JSON object: %w", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("skills: Reflect changelog metadata must be a JSON object")
	}
	// Canonical round-trip rejects unsupported values and makes the persisted
	// object independent of the caller's map/RawMessage backing storage.
	canonical, err := canonicalJSON(object)
	if err != nil {
		return nil, fmt.Errorf("skills: canonicalize Reflect changelog metadata: %w", err)
	}
	return homeStoreMetadataMap(canonical)
}

func homeReflectSnapshotMatches(snapshot HomeManagedSkillSnapshot, description string, disable bool, metadata map[string]any, files []HomeSkillFile) bool {
	if snapshot.Skill.Description != description || snapshot.Skill.DisableModelInvocation != disable || snapshot.Skill.Status != SkillStatusActive || CreatedBy(snapshot.Skill) != ReflectSkillCreatedBy {
		return false
	}
	actual, err := homeStoreMetadataMap(snapshot.Skill.Metadata)
	if err != nil || !homeReflectMetadataEqual(actual, metadata) {
		return false
	}
	if len(snapshot.Files) != len(files) {
		return false
	}
	for i := range files {
		if snapshot.Files[i].Path != files[i].Path || snapshot.Files[i].Mode != files[i].Mode || string(snapshot.Files[i].Content) != string(files[i].Content) {
			return false
		}
	}
	return true
}

func homeReflectMetadataEqual(left, right map[string]any) bool {
	leftJSON, leftErr := canonicalJSON(left)
	rightJSON, rightErr := canonicalJSON(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func reflectPatchDescription(before HomeManagedSkillSnapshot, request HomeSkillUpdateRequest) string {
	if request.Description != nil {
		return *request.Description
	}
	return before.Skill.Description
}

func reflectPatchDisable(before HomeManagedSkillSnapshot, request HomeSkillUpdateRequest) bool {
	if request.DisableModelInvocation != nil {
		return *request.DisableModelInvocation
	}
	return before.Skill.DisableModelInvocation
}

func reflectPatchFiles(before HomeManagedSkillSnapshot, request HomeSkillUpdateRequest) []HomeSkillFile {
	files, err := mergeHomeSkillFiles(before.Files, request.FileUpserts, nil)
	if err != nil {
		return nil
	}
	description := reflectPatchDescription(before, request)
	if canonicalizeSkillMainFile(files, before.Skill.Name, description) != nil {
		return nil
	}
	return homeSkillFiles(files)
}

func homeReflectUsageIdentity(skill Skill) HomeSkillUsageIdentity {
	return HomeSkillUsageIdentity{ID: skill.ID, UserID: skill.UserID, AgentID: skill.AgentID, Name: skill.Name, LastContentDigest: skill.ContentDigest}
}

func newHomeReflectUsageIdentity(id, userID, agentID, digest string) (HomeSkillUsageIdentity, error) {
	scope, decodedUserID, decodedAgentID, name, err := decodeFilesystemSkillID(id)
	if err != nil {
		return HomeSkillUsageIdentity{}, err
	}
	if scope != "user_agent" || decodedUserID != userID || decodedAgentID != agentID {
		return HomeSkillUsageIdentity{}, errors.New("skills: runtime Reflect usage identity does not match canonical Skill ID")
	}
	return HomeSkillUsageIdentity{ID: id, UserID: userID, AgentID: agentID, Name: name, LastContentDigest: digest}, nil
}

func regularSkillMode() *fs.FileMode {
	mode := fs.FileMode(0o644)
	return &mode
}

func reflectTelemetryOutcome(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", sandbox.ErrOutcomeUnknown, action, err)
}

var _ interface {
	CreateReflectOwnedUserAgentSkill(context.Context, ReflectSkillCreate) (Skill, error)
	PatchReflectOwnedUserAgentSkill(context.Context, ReflectSkillPatch) (Skill, error)
	DeleteReflectOwnedUserAgentSkill(context.Context, ReflectSkillDelete) (Skill, error)
} = (*HomeReflectStore)(nil)
