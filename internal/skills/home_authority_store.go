package skills

import (
	"context"
	"errors"
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
	return s.home.UpdateManagedSkill(ctx, in)
}

func (s *HomeAuthorityStore) DeleteManagedSkill(ctx context.Context, in ManagedSkillDelete) error {
	return s.home.DeleteManagedSkill(ctx, in)
}

func (s *HomeAuthorityStore) DeleteManagedSkillFile(ctx context.Context, in ManagedSkillFileDelete) (SkillSnapshot, error) {
	return s.home.DeleteManagedSkillFile(ctx, in)
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

func (s *HomeAuthorityStore) ListFiles(ctx context.Context, id string) ([]string, error) {
	return s.home.ListFiles(ctx, id)
}

func (s *HomeAuthorityStore) ListFilesWithContent(ctx context.Context, id string) (map[string]string, error) {
	return s.home.ListFilesWithContent(ctx, id)
}

var _ Store = (*HomeAuthorityStore)(nil)
