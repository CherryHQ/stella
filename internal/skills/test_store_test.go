package skills

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// testSkillStore is a Home-authoritative adapter for active runtime tests.
type testSkillStore struct {
	*HomeAuthorityStore
	db *pgxpool.Pool
}

func newTestSkillStore(t *testing.T) (*testSkillStore, string, string) {
	t.Helper()
	store, db, _ := newTestStore(t)
	userID, agentID := seedFixtures(t, db)
	return &testSkillStore{HomeAuthorityStore: store, db: db}, userID, agentID
}

func pluginSkill(sk Skill) pkgplugins.Skill {
	return pkgplugins.Skill{ID: sk.ID, Scope: sk.Scope, UserID: sk.UserID, AgentID: sk.AgentID, Name: sk.Name, Description: sk.Description, Status: sk.Status, DisableModelInvocation: sk.DisableModelInvocation, Metadata: sk.Metadata, CreatedAt: sk.CreatedAt, UpdatedAt: sk.UpdatedAt, Version: sk.Version, ContentDigest: sk.ContentDigest}
}

func internalSkill(sk pkgplugins.Skill) Skill {
	out := Skill{ID: sk.ID, Scope: sk.Scope, UserID: sk.UserID, AgentID: sk.AgentID, Name: sk.Name, Description: sk.Description, Status: sk.Status, DisableModelInvocation: sk.DisableModelInvocation, Metadata: sk.Metadata, ContentDigest: sk.ContentDigest}
	switch out.Scope {
	case "system":
		out.UserID, out.AgentID = "", ""
	case "system_agent":
		out.UserID = ""
	case "user":
		out.AgentID = ""
	}
	return out
}

func pluginSnapshot(s SkillSnapshot) pkgplugins.ManagedSkillSnapshot {
	return pkgplugins.ManagedSkillSnapshot{Skill: pluginSkill(s.Skill), Files: s.Files}
}

func (s *testSkillStore) List(ctx context.Context, vc pkgplugins.SkillViewContext) ([]pkgplugins.Skill, error) {
	rows, e := s.HomeAuthorityStore.List(ctx, ViewContext{UserID: vc.UserID, AgentID: vc.AgentID})
	out := make([]pkgplugins.Skill, len(rows))
	for i := range rows {
		out[i] = pluginSkill(rows[i])
	}
	return out, e
}

func (s *testSkillStore) Resolve(ctx context.Context, n string, vc pkgplugins.SkillViewContext) (*pkgplugins.Skill, error) {
	sk, e := s.HomeAuthorityStore.Resolve(ctx, n, ViewContext{UserID: vc.UserID, AgentID: vc.AgentID})
	if sk == nil {
		return nil, e
	}
	out := pluginSkill(*sk)
	return &out, e
}

func (s *testSkillStore) ListByScope(ctx context.Context, scope, user, agent string) ([]pkgplugins.Skill, error) {
	rows, e := s.HomeAuthorityStore.ListByScope(ctx, scope, user, agent)
	out := make([]pkgplugins.Skill, len(rows))
	for i := range rows {
		out[i] = pluginSkill(rows[i])
	}
	return out, e
}

func (s *testSkillStore) LoadFile(ctx context.Context, id, p string) (string, error) {
	return s.HomeAuthorityStore.LoadFile(ctx, id, p)
}

func (s *testSkillStore) ListFiles(ctx context.Context, id string) ([]string, error) {
	return s.HomeAuthorityStore.ListFiles(ctx, id)
}

func (s *testSkillStore) Create(ctx context.Context, sk pkgplugins.Skill, files map[string]string) (string, error) {
	if sk.Description == "" {
		sk.Description = "x"
	}
	if body := files[MainFile]; !strings.HasPrefix(body, "---\n") || !strings.Contains(strings.SplitN(body, "---\n", 3)[1], "description:") {
		files = cloneTestSkillFiles(files)
		files[MainFile] = "---\nname: " + sk.Name + "\ndescription: " + sk.Description + "\n---\n" + body
	}
	if IsReflectOwned(internalSkill(sk)) && sk.Scope == "user_agent" {
		created, err := s.CreateReflectOwnedUserAgentSkill(ctx, ReflectSkillCreate{UserID: sk.UserID, AgentID: sk.AgentID, Name: sk.Name, Description: sk.Description, MainFileContent: files[MainFile], Metadata: sk.Metadata})
		return created.ID, err
	}
	snap, e := s.HomeAuthorityStore.CreateManagedSkill(ctx, internalSkill(sk), files)
	return snap.Skill.ID, e
}

func cloneTestSkillFiles(files map[string]string) map[string]string {
	out := make(map[string]string, len(files))
	maps.Copy(out, files)
	return out
}

func (s *testSkillStore) CreateManagedSkill(ctx context.Context, sk pkgplugins.Skill, files map[string]string) (pkgplugins.ManagedSkillSnapshot, error) {
	snap, e := s.HomeAuthorityStore.CreateManagedSkill(ctx, internalSkill(sk), files)
	return pluginSnapshot(snap), e
}

func (s *testSkillStore) UpdateManagedSkill(ctx context.Context, in pkgplugins.ManagedSkillUpdate) (pkgplugins.ManagedSkillSnapshot, error) {
	snap, e := s.HomeAuthorityStore.UpdateManagedSkill(ctx, ManagedSkillUpdate{ID: in.ID, UserID: in.UserID, AgentID: in.AgentID, Scope: in.Scope, ExpectedDigest: in.ExpectedDigest, Patch: UpdatePatch{Description: in.Patch.Description, DisableModelInvocation: in.Patch.DisableModelInvocation, Metadata: in.Patch.Metadata}, Files: in.Files, DeleteFiles: in.DeleteFiles, ConvertToManual: in.ConvertToManual})
	return pluginSnapshot(snap), e
}

func (s *testSkillStore) DeleteManagedSkill(ctx context.Context, in pkgplugins.ManagedSkillDelete) error {
	return s.HomeAuthorityStore.DeleteManagedSkill(ctx, ManagedSkillDelete{ID: in.ID, UserID: in.UserID, AgentID: in.AgentID, Scope: in.Scope, ExpectedDigest: in.ExpectedDigest})
}

func (s *testSkillStore) DeleteManagedSkillFile(ctx context.Context, in pkgplugins.ManagedSkillFileDelete) (pkgplugins.ManagedSkillSnapshot, error) {
	snap, e := s.HomeAuthorityStore.DeleteManagedSkillFile(ctx, ManagedSkillFileDelete{ManagedSkillDelete: ManagedSkillDelete{ID: in.ID, UserID: in.UserID, AgentID: in.AgentID, Scope: in.Scope, ExpectedDigest: in.ExpectedDigest}, Path: in.Path})
	return pluginSnapshot(snap), e
}

func (s *testSkillStore) UpsertFile(ctx context.Context, id, path, content string) error {
	sk, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.HomeAuthorityStore.UpdateManagedSkill(ctx, ManagedSkillUpdate{ID: sk.ID, Scope: sk.Scope, UserID: sk.UserID, AgentID: sk.AgentID, ExpectedDigest: sk.ContentDigest, Files: map[string]string{path: content}})
	return err
}

type recordingSkillStore struct {
	*testSkillStore
	deleteCalls, updateCalls int
	lastDelete               pkgplugins.ManagedSkillDelete
	lastUpdate               pkgplugins.ManagedSkillUpdate
	digest                   string
}

func (s *recordingSkillStore) ListByScope(ctx context.Context, scope, userID, agentID string) ([]pkgplugins.Skill, error) {
	rows, err := s.testSkillStore.ListByScope(ctx, scope, userID, agentID)
	for i := range rows {
		rows[i].ContentDigest = s.digest
	}
	return rows, err
}

func (s *recordingSkillStore) DeleteManagedSkill(_ context.Context, in pkgplugins.ManagedSkillDelete) error {
	s.deleteCalls++
	s.lastDelete = in
	return nil
}

func (s *recordingSkillStore) UpdateManagedSkill(_ context.Context, in pkgplugins.ManagedSkillUpdate) (pkgplugins.ManagedSkillSnapshot, error) {
	s.updateCalls++
	s.lastUpdate = in
	return pkgplugins.ManagedSkillSnapshot{}, nil
}
