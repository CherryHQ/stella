package skills

import (
	"context"
	"errors"
	"io/fs"
)

// HomeAuthorityStore composes the one Home current-state authority with its
// Reflect telemetry companion. It is intentionally free of PostgreSQL Skill
// current-state and changelog dependencies.
type HomeAuthorityStore struct {
	home    *HomeStore
	reflect *HomeReflectStore
}

func NewHomeAuthorityStore(home *HomeStore, reflectStore *HomeReflectStore) (*HomeAuthorityStore, error) {
	if home == nil || reflectStore == nil || reflectStore.home == nil {
		return nil, errors.New("skills: Home authority store requires Home and Reflect stores")
	}
	if reflectStore.home != home {
		return nil, errors.New("skills: Home authority store cannot split current-state authority")
	}
	return &HomeAuthorityStore{home: home, reflect: reflectStore}, nil
}

func (s *HomeAuthorityStore) Get(ctx context.Context, id string) (*Skill, error) {
	return s.home.Get(ctx, id)
}

func (s *HomeAuthorityStore) List(ctx context.Context, vc ViewContext) ([]Skill, error) {
	return s.home.List(ctx, vc)
}

func (s *HomeAuthorityStore) ListAll(ctx context.Context) ([]Skill, error) {
	return s.home.ListAll(ctx)
}

func (s *HomeAuthorityStore) ListActiveReflectOwnedUserAgentSkills(ctx context.Context, userID, agentID string) ([]Skill, error) {
	return s.home.ListActiveReflectOwnedUserAgentSkills(ctx, userID, agentID)
}

func (s *HomeAuthorityStore) CreateManagedSkill(ctx context.Context, sk Skill, files map[string]string) (SkillSnapshot, error) {
	return s.home.CreateManagedSkill(ctx, sk, files)
}

func (s *HomeAuthorityStore) UpdateManagedSkill(ctx context.Context, in ManagedSkillUpdate) (SkillSnapshot, error) {
	if _, err := homeStoreUpdateRequest(in); err != nil {
		return SkillSnapshot{}, err
	}
	before, err := s.loadManagedSnapshotForTelemetry(ctx, in.ID)
	if err != nil {
		return SkillSnapshot{}, err
	}
	identity, tracked, err := homeReflectTelemetryIdentity(before.Skill)
	if err != nil {
		return SkillSnapshot{}, err
	}
	after, err := s.home.UpdateManagedSkill(ctx, in)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if !tracked {
		return after, nil
	}
	if in.ConvertToManual {
		if err := s.reflect.usage.DeleteForLifecycle(ctx, identity); err != nil {
			return SkillSnapshot{}, reflectTelemetryOutcome("delete Home Reflect lifecycle usage", err)
		}
		return after, nil
	}
	if _, err := s.reflect.usage.PatchReflectDigest(ctx, identity, after.Skill.ContentDigest); err != nil {
		return SkillSnapshot{}, reflectTelemetryOutcome("patch Home Reflect usage after managed update", err)
	}
	return after, nil
}

func (s *HomeAuthorityStore) DeleteManagedSkill(ctx context.Context, in ManagedSkillDelete) error {
	if err := validateHomeManagedDelete(in); err != nil {
		return err
	}
	before, err := s.loadManagedSnapshotForTelemetry(ctx, in.ID)
	if err != nil {
		return err
	}
	identity, tracked, err := homeReflectTelemetryIdentity(before.Skill)
	if err != nil {
		return err
	}
	if err := s.home.DeleteManagedSkill(ctx, in); err != nil {
		return err
	}
	if tracked {
		if err := s.reflect.usage.DeleteForLifecycle(ctx, identity); err != nil {
			return reflectTelemetryOutcome("delete Home Reflect lifecycle usage", err)
		}
	}
	return nil
}

func (s *HomeAuthorityStore) DeleteManagedSkillFile(ctx context.Context, in ManagedSkillFileDelete) (SkillSnapshot, error) {
	if err := validateHomeManagedDelete(in.ManagedSkillDelete); err != nil {
		return SkillSnapshot{}, err
	}
	if err := validateHomeMutationPath(in.Path); err != nil {
		return SkillSnapshot{}, err
	}
	if in.Path == MainFile {
		return SkillSnapshot{}, errors.New("skills: SKILL.md cannot be deleted")
	}
	before, err := s.loadManagedSnapshotForTelemetry(ctx, in.ID)
	if err != nil {
		return SkillSnapshot{}, err
	}
	identity, tracked, err := homeReflectTelemetryIdentity(before.Skill)
	if err != nil {
		return SkillSnapshot{}, err
	}
	after, err := s.home.DeleteManagedSkillFile(ctx, in)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if tracked {
		if _, err := s.reflect.usage.PatchReflectDigest(ctx, identity, after.Skill.ContentDigest); err != nil {
			return SkillSnapshot{}, reflectTelemetryOutcome("patch Home Reflect usage after managed file delete", err)
		}
	}
	return after, nil
}

// loadManagedSnapshotForTelemetry uses Home's canonical snapshot solely to
// decide whether a successful generic mutation requires derived telemetry work.
// The manager's digest CAS remains the race arbiter for the subsequent mutation.
func (s *HomeAuthorityStore) loadManagedSnapshotForTelemetry(ctx context.Context, id string) (HomeManagedSkillSnapshot, error) {
	snapshot, err := s.home.catalog.LoadManagedSnapshot(ctx, id)
	if errors.Is(err, fs.ErrNotExist) {
		return HomeManagedSkillSnapshot{}, ErrHomeSkillConflict
	}
	return snapshot, err
}

func homeReflectTelemetryIdentity(skill Skill) (HomeSkillUsageIdentity, bool, error) {
	if !IsReflectOwned(skill) {
		return HomeSkillUsageIdentity{}, false, nil
	}
	identity, err := newHomeReflectUsageIdentity(skill.ID, skill.UserID, skill.AgentID, skill.ContentDigest)
	if err != nil {
		return HomeSkillUsageIdentity{}, false, err
	}
	return identity, true, nil
}

func (s *HomeAuthorityStore) CreateReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillCreate) (Skill, error) {
	return s.reflect.CreateReflectOwnedUserAgentSkill(ctx, in)
}

func (s *HomeAuthorityStore) PatchReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillPatch) (Skill, error) {
	return s.reflect.PatchReflectOwnedUserAgentSkill(ctx, in)
}

func (s *HomeAuthorityStore) DeleteReflectOwnedUserAgentSkill(ctx context.Context, in ReflectSkillDelete) (Skill, error) {
	return s.reflect.DeleteReflectOwnedUserAgentSkill(ctx, in)
}

func (s *HomeAuthorityStore) TouchReflectSkillRuntimeUse(ctx context.Context, id, userID, agentID, digest string) error {
	return s.reflect.TouchReflectSkillRuntimeUse(ctx, id, userID, agentID, digest)
}

func (s *HomeAuthorityStore) ListForAgentContext(ctx context.Context, userID, agentID string) ([]Skill, error) {
	return s.home.ListForAgentContext(ctx, userID, agentID)
}

func (s *HomeAuthorityStore) ListByScope(ctx context.Context, scope, userID, agentID string) ([]Skill, error) {
	return s.home.ListByScope(ctx, scope, userID, agentID)
}

func (s *HomeAuthorityStore) ListForAdmin(ctx context.Context, userID string) ([]Skill, error) {
	return s.home.ListForAdmin(ctx, userID)
}

func (s *HomeAuthorityStore) ListForUser(ctx context.Context, userID string, agentIDs []string) ([]Skill, error) {
	return s.home.ListForUser(ctx, userID, agentIDs)
}

func (s *HomeAuthorityStore) Resolve(ctx context.Context, name string, vc ViewContext) (*Skill, error) {
	return s.home.Resolve(ctx, name, vc)
}

func (s *HomeAuthorityStore) LoadFile(ctx context.Context, id, path string) (string, error) {
	return s.home.LoadFile(ctx, id, path)
}

func (s *HomeAuthorityStore) LoadResolvedFile(ctx context.Context, name, path string, vc ViewContext) (*HomeSkillLoad, error) {
	return s.home.LoadResolvedFile(ctx, name, path, vc)
}

func (s *HomeAuthorityStore) ListFiles(ctx context.Context, id string) ([]string, error) {
	return s.home.ListFiles(ctx, id)
}

func (s *HomeAuthorityStore) ListFilesWithContent(ctx context.Context, id string) (map[string]string, error) {
	return s.home.ListFilesWithContent(ctx, id)
}

var _ Store = (*HomeAuthorityStore)(nil)
