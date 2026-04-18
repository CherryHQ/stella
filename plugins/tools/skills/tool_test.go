package skills

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// testSkillStore is a minimal pkgplugins.SkillStore backed directly by sqlc.
// It avoids the import cycle internal/skills → plugins/tools/skills → internal/skills.
type testSkillStore struct {
	db *sql.DB
	q  *sqlc.Queries
}

func newTestSkillStore(t *testing.T) (*testSkillStore, int64, string, string) {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	authStore := appdb.NewAuthStore(db)
	u, err := authStore.CreateUser(ctx, "testuser", "hash")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	agentID := "agent1"
	cs := config.NewDBStore(db)
	if err := cs.CreateAgent(ctx, config.Agent{
		ID: agentID, Name: agentID, Model: "p/m", Workspace: "/tmp/" + agentID, Enabled: true,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	projectRoot := t.TempDir()
	return &testSkillStore{db: db, q: sqlc.New(db)}, u.ID, agentID, projectRoot
}

func (s *testSkillStore) List(ctx context.Context, vc pkgplugins.SkillViewContext) ([]pkgplugins.Skill, error) {
	rows, err := s.q.ListSkillsVisible(ctx, sqlc.ListSkillsVisibleParams{
		AgentID: sql.NullString{String: vc.AgentID, Valid: vc.AgentID != ""},
		UserID:  sql.NullInt64{Int64: vc.UserID, Valid: vc.UserID != 0},
		Project: sql.NullString{String: vc.Project, Valid: vc.Project != ""},
	})
	if err != nil {
		return nil, err
	}
	out := make([]pkgplugins.Skill, 0, len(rows))
	for _, r := range rows {
		out = append(out, tsMapRow(r))
	}
	return out, nil
}

func (s *testSkillStore) Resolve(ctx context.Context, name string, vc pkgplugins.SkillViewContext) (*pkgplugins.Skill, error) {
	row, err := s.q.ResolveSkill(ctx, sqlc.ResolveSkillParams{
		Name:    name,
		AgentID: sql.NullString{String: vc.AgentID, Valid: vc.AgentID != ""},
		UserID:  sql.NullInt64{Int64: vc.UserID, Valid: vc.UserID != 0},
		Project: sql.NullString{String: vc.Project, Valid: vc.Project != ""},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sk := tsMapRow(row)
	return &sk, nil
}

func (s *testSkillStore) LoadFile(ctx context.Context, skillID, path string) (string, error) {
	f, err := s.q.GetSkillFile(ctx, sqlc.GetSkillFileParams{SkillID: skillID, Path: path})
	if err != nil {
		return "", err
	}
	return f.Content, nil
}

func (s *testSkillStore) Create(ctx context.Context, sk pkgplugins.Skill, files map[string]string) (string, error) {
	if sk.ID == "" {
		sk.ID = uuid.New().String()[:8]
	}
	if sk.Status == "" {
		sk.Status = "active"
	}
	meta := "{}"
	if len(sk.Metadata) > 0 {
		meta = string(sk.Metadata)
	}
	disabled := int64(0)
	if sk.DisableModelInvocation {
		disabled = 1
	}
	params := sqlc.CreateSkillParams{
		ID:                     sk.ID,
		Scope:                  sk.Scope,
		Name:                   sk.Name,
		Description:            sk.Description,
		Status:                 sk.Status,
		DisableModelInvocation: disabled,
		Metadata:               meta,
	}
	switch sk.Scope {
	case "user":
		params.UserID = sql.NullInt64{Int64: sk.UserID, Valid: true}
	case "agent":
		params.AgentID = sql.NullString{String: sk.AgentID, Valid: true}
	case "project":
		params.Project = sql.NullString{String: sk.Project, Valid: true}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.CreateSkill(ctx, params); err != nil {
		return "", err
	}
	for path, content := range files {
		if err := qtx.UpsertSkillFile(ctx, sqlc.UpsertSkillFileParams{SkillID: sk.ID, Path: path, Content: content}); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return sk.ID, nil
}

func (s *testSkillStore) Update(ctx context.Context, id string, patch pkgplugins.SkillUpdatePatch) error {
	row, err := s.q.GetSkill(ctx, id)
	if err != nil {
		return err
	}
	desc := row.Description
	if patch.Description != nil {
		desc = *patch.Description
	}
	status := row.Status
	if patch.Status != nil {
		status = *patch.Status
	}
	disabled := row.DisableModelInvocation
	if patch.DisableModelInvocation != nil {
		if *patch.DisableModelInvocation {
			disabled = 1
		} else {
			disabled = 0
		}
	}
	meta := row.Metadata
	if len(patch.Metadata) > 0 {
		meta = string(patch.Metadata)
	}
	return s.q.UpdateSkillMetadata(ctx, sqlc.UpdateSkillMetadataParams{
		ID: id, Description: desc, Status: status, DisableModelInvocation: disabled, Metadata: meta,
	})
}

func (s *testSkillStore) UpsertFile(ctx context.Context, skillID, path, content string) error {
	return s.q.UpsertSkillFile(ctx, sqlc.UpsertSkillFileParams{SkillID: skillID, Path: path, Content: content})
}

func (s *testSkillStore) DeleteFile(ctx context.Context, skillID, path string) error {
	return s.q.DeleteSkillFile(ctx, sqlc.DeleteSkillFileParams{SkillID: skillID, Path: path})
}

func (s *testSkillStore) Delete(ctx context.Context, id string) error {
	return s.q.DeleteSkill(ctx, id)
}

func (s *testSkillStore) ExpireDrafts(ctx context.Context, before time.Time) error {
	return s.q.DeprecateExpiredDrafts(ctx, before.UTC().Format(time.RFC3339))
}

func tsMapRow(r sqlc.Skill) pkgplugins.Skill {
	meta := json.RawMessage("{}")
	if r.Metadata != "" {
		meta = json.RawMessage(r.Metadata)
	}
	return pkgplugins.Skill{
		ID:                     r.ID,
		Scope:                  r.Scope,
		UserID:                 r.UserID.Int64,
		AgentID:                r.AgentID.String,
		Project:                r.Project.String,
		Name:                   r.Name,
		Description:            r.Description,
		Status:                 r.Status,
		DisableModelInvocation: r.DisableModelInvocation != 0,
		Metadata:               meta,
	}
}

// ctxWithUser returns a context carrying userID and agentID.
func ctxWithUser(userID int64, agentID string) context.Context {
	ctx := context.Background()
	ctx = memory.WithUserID(ctx, userID)
	ctx = memory.WithAgentID(ctx, agentID)
	return ctx
}

func TestDefinition(t *testing.T) {
	tool := NewTool(nil, "/tmp/anna", "/tmp/agents", "", "", nil)
	def := tool.Definition()

	if def.Name != "skills" {
		t.Errorf("expected name 'skills', got %q", def.Name)
	}
	if def.Description == "" {
		t.Error("expected non-empty description")
	}
	if def.InputSchema == nil {
		t.Error("expected non-nil input schema")
	}
}

func TestExecuteUnknownAction(t *testing.T) {
	tool := NewTool(nil, "/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "bogus"})
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestExecuteDispatch(t *testing.T) {
	tool := NewTool(nil, "/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "search"})
	if err == nil {
		t.Error("expected error for search without query")
	}
	_, err = tool.Execute(context.Background(), map[string]any{"action": "install"})
	if err == nil {
		t.Error("expected error for install without source")
	}
	_, err = tool.Execute(context.Background(), map[string]any{"action": "remove"})
	if err == nil {
		t.Error("expected error for remove without name")
	}
}

func TestSearchMissingQuery(t *testing.T) {
	tool := NewTool(nil, "/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.search(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing query")
	}
}

func TestInstallMissingSource(t *testing.T) {
	tool := NewTool(nil, "/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.install(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing source")
	}
}

func TestRemoveMissingName(t *testing.T) {
	tool := NewTool(nil, "/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.remove(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestRemoveInvalidName(t *testing.T) {
	tool := NewTool(nil, "/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.remove(context.Background(), map[string]any{"name": "../../../etc"})
	if err == nil {
		t.Error("expected error for path traversal name")
	}
}

func TestTargetSkillsDirDefaultsToUserScope(t *testing.T) {
	base := t.TempDir()
	agentWS := filepath.Join(base, "workspaces", "agent-1")
	userSkillsDir := filepath.Join(agentWS, "users", "7", ".agents", "skills")

	tool := NewTool(nil, "/tmp/anna", agentWS, filepath.Join(base, "project"), userSkillsDir, nil)
	scope, got, err := tool.targetSkillsDir(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scope != skillScopeUser {
		t.Fatalf("scope = %q, want %q", scope, skillScopeUser)
	}
	if got != userSkillsDir {
		t.Errorf("targetSkillsDir() = %q, want %q", got, userSkillsDir)
	}
}

func TestTargetSkillsDirProjectScope(t *testing.T) {
	base := t.TempDir()
	agentWS := filepath.Join(base, "workspaces", "agent-1")
	projectRoot := filepath.Join(base, "project")

	tool := NewTool(nil, "/tmp/anna", agentWS, projectRoot, filepath.Join(agentWS, "users", "7", ".agents", "skills"), nil)
	scope, got, err := tool.targetSkillsDir(context.Background(), "project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scope != skillScopeProject {
		t.Fatalf("scope = %q, want %q", scope, skillScopeProject)
	}
	want := filepath.Join(projectRoot, ".agents", "skills")
	if got != want {
		t.Errorf("targetSkillsDir() = %q, want %q", got, want)
	}
}

func TestInstallProjectScopeRequiresProjectRoot(t *testing.T) {
	store, _, _, _ := newTestSkillStore(t)
	userSkillsDir := t.TempDir()

	tool := NewTool(store, "/tmp/anna", t.TempDir(), "", userSkillsDir, nil)
	_, err := tool.install(context.Background(), map[string]any{
		"source": t.TempDir(),
		"scope":  "project",
	})
	if err == nil {
		t.Fatal("expected error when project scope is requested without ProjectRoot")
	}
	if !strings.Contains(err.Error(), "ProjectRoot") {
		t.Fatalf("expected ProjectRoot error, got %v", err)
	}
}

func TestInstallRejectsNonStringScope(t *testing.T) {
	// scope parse happens before store check
	tool := NewTool(nil, "/tmp/anna", t.TempDir(), t.TempDir(), t.TempDir(), nil)
	_, err := tool.install(context.Background(), map[string]any{
		"source": t.TempDir(),
		"scope":  1,
	})
	if err == nil {
		t.Fatal("expected error when scope is not a string")
	}
	if !strings.Contains(err.Error(), "scope must be a string") {
		t.Fatalf("expected scope type error, got %v", err)
	}
}

// --- Store-backed tests ---

func TestLoadViaStore(t *testing.T) {
	store, userID, agentID, _ := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "test-skill",
		Description: "A test skill",
		Status:      "active",
	}, map[string]string{
		pkgplugins.SkillMainFile: "---\nname: test-skill\ndescription: A test skill\n---\n# Test Skill\nDo the thing.",
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	tool := NewTool(store, "", "", "", "", nil)

	t.Run("loads existing skill default path", func(t *testing.T) {
		result, err := tool.load(ctx, map[string]any{"name": "test-skill"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "# Test Skill") {
			t.Error("expected skill content in result")
		}
		if !strings.Contains(result, `path="SKILL.md"`) {
			t.Errorf("expected path= in result, got %q", result)
		}
		if strings.Contains(result, "base_dir=") {
			t.Error("expected no base_dir= in result (now uses path=)")
		}
	})

	t.Run("unknown skill", func(t *testing.T) {
		_, err := tool.load(ctx, map[string]any{"name": "nonexistent"})
		if err == nil {
			t.Fatal("expected error for unknown skill")
		}
	})

	t.Run("missing name", func(t *testing.T) {
		_, err := tool.load(ctx, map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing name")
		}
	})
}

func TestLoadWithPath(t *testing.T) {
	store, userID, agentID, _ := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	skillID, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "multi-file",
		Description: "Skill with multiple files",
		Status:      "active",
	}, map[string]string{
		pkgplugins.SkillMainFile: "# Multi File Skill",
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	if err := store.UpsertFile(ctx, skillID, "references/api.md", "# API Reference\nEndpoints here."); err != nil {
		t.Fatalf("upsert file: %v", err)
	}

	tool := NewTool(store, "", "", "", "", nil)
	result, err := tool.load(ctx, map[string]any{"name": "multi-file", "path": "references/api.md"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, `path="references/api.md"`) {
		t.Errorf("expected path in result, got %q", result)
	}
	if !strings.Contains(result, "# API Reference") {
		t.Errorf("expected file content in result, got %q", result)
	}
}

func TestListViaStore(t *testing.T) {
	store, userID, agentID, _ := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "my-skill",
		Description: "My skill",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# My Skill"})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	tool := NewTool(store, "", "", "", "", nil)
	result, err := tool.list(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var skills []installedSkill
	if err := json.Unmarshal([]byte(result), &skills); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	var found bool
	for _, s := range skills {
		if s.Name == "my-skill" {
			found = true
			if s.Description != "My skill" {
				t.Errorf("expected description 'My skill', got %q", s.Description)
			}
			if s.Scope != "user" {
				t.Errorf("expected scope 'user', got %q", s.Scope)
			}
			if !s.Removable {
				t.Error("expected removable=true for user-scoped skill")
			}
		}
	}
	if !found {
		t.Error("expected my-skill to appear in list results")
	}
}

func TestRemoveViaStore(t *testing.T) {
	store, userID, agentID, _ := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "removable-skill",
		Description: "Removable",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# Removable"})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	tool := NewTool(store, "", "", "", "", nil)
	result, err := tool.remove(ctx, map[string]any{"name": "removable-skill"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "removable-skill") {
		t.Errorf("expected skill name in result, got %q", result)
	}

	// Verify gone
	_, err = tool.remove(ctx, map[string]any{"name": "removable-skill"})
	if err == nil {
		t.Error("expected error after double remove")
	}
}

func TestInstallFromLocalDirViaStore(t *testing.T) {
	srcDir := t.TempDir()
	skillDir := filepath.Join(srcDir, "store-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: store-skill\ndescription: Store skill test\nstatus: active\n---\n# Store Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, userID, agentID, _ := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	userSkillsDir := t.TempDir()
	tool := NewTool(store, "", "", "", userSkillsDir, nil)
	result, err := tool.install(ctx, map[string]any{"source": srcDir})
	if err != nil {
		t.Fatalf("install error: %v", err)
	}
	if !strings.Contains(result, "scope=user") {
		t.Fatalf("expected user scope in result, got %q", result)
	}

	// Verify it's in the store.
	vc := pkgplugins.SkillViewContext{UserID: userID}
	skills, err := store.List(ctx, vc)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, s := range skills {
		if s.Name == "store-skill" {
			found = true
			if s.Scope != "user" {
				t.Errorf("expected scope 'user', got %q", s.Scope)
			}
		}
	}
	if !found {
		t.Error("expected store-skill in store after install")
	}
}
