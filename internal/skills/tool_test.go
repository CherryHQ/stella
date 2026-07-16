package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// testSkillStore is a minimal pkgplugins.SkillStore backed directly by sqlc.
type testSkillStore struct {
	db *pgxpool.Pool
	q  *sqlc.Queries
}

func newTestSkillStore(t *testing.T) (*testSkillStore, string, string) {
	t.Helper()
	db := dbtest.New(t)

	ctx := context.Background()
	oidcStore := appdb.NewOIDCStore(db)
	u, err := oidcStore.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: "testuser@test.local",
		Name:  "testuser",
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	agentID := "agent1"
	cs := store.NewDBStore(db)
	if err := cs.CreateAgent(ctx, config.Agent{
		ID: agentID, Name: agentID, Model: "p/m", Workspace: "/tmp/" + agentID, Enabled: true,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	return &testSkillStore{db: db, q: sqlc.New(db)}, u.ID, agentID
}

func (s *testSkillStore) List(ctx context.Context, vc pkgplugins.SkillViewContext) ([]pkgplugins.Skill, error) {
	rows, err := s.q.ListSkillsVisible(ctx, sqlc.ListSkillsVisibleParams{
		AgentID: pgtype.Text{String: vc.AgentID, Valid: vc.AgentID != ""},
		UserID:  pgtype.Text{String: vc.UserID, Valid: vc.UserID != ""},
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
		AgentID: pgtype.Text{String: vc.AgentID, Valid: vc.AgentID != ""},
		UserID:  pgtype.Text{String: vc.UserID, Valid: vc.UserID != ""},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sk := tsMapRow(row)
	return &sk, nil
}

func (s *testSkillStore) ListByScope(ctx context.Context, scope, userID, agentID string) ([]pkgplugins.Skill, error) {
	rows, err := s.q.ListSkillsByScope(ctx, sqlc.ListSkillsByScopeParams{
		Scope:   scope,
		UserID:  pgtype.Text{String: userID, Valid: userID != ""},
		AgentID: pgtype.Text{String: agentID, Valid: agentID != ""},
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

func (s *testSkillStore) ListFiles(ctx context.Context, skillID string) ([]string, error) {
	rows, err := s.q.ListSkillFiles(ctx, skillID)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(rows))
	for _, r := range rows {
		paths = append(paths, r.Path)
	}
	return paths, nil
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
	meta := json.RawMessage("{}")
	if len(sk.Metadata) > 0 {
		meta = sk.Metadata
	}
	params := sqlc.CreateSkillParams{
		ID:                     sk.ID,
		Scope:                  sk.Scope,
		Name:                   sk.Name,
		Description:            sk.Description,
		Status:                 sk.Status,
		DisableModelInvocation: sk.DisableModelInvocation,
		Metadata:               meta,
	}
	switch sk.Scope {
	case "user":
		params.UserID = pgtype.Text{String: sk.UserID, Valid: true}
	case "user_agent":
		params.UserID = pgtype.Text{String: sk.UserID, Valid: true}
		params.AgentID = pgtype.Text{String: sk.AgentID, Valid: true}
	case "system_agent":
		params.AgentID = pgtype.Text{String: sk.AgentID, Valid: true}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.CreateSkill(ctx, params); err != nil {
		return "", err
	}
	for path, content := range files {
		if err := qtx.UpsertSkillFile(ctx, sqlc.UpsertSkillFileParams{SkillID: sk.ID, Path: path, Content: content}); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return sk.ID, nil
}

func (s *testSkillStore) Update(ctx context.Context, id string, patch pkgplugins.SkillUpdatePatch) error {
	row, err := s.q.GetSkill(ctx, sqlc.GetSkillParams{ID: id, AgentID: pgtype.Text{}, UserID: pgtype.Text{}})
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
		disabled = *patch.DisableModelInvocation
	}
	meta := row.Metadata
	if len(patch.Metadata) > 0 {
		meta = patch.Metadata
	}
	params := sqlc.UpdateSkillMetadataParams{
		ID: id, Description: desc, Status: status, DisableModelInvocation: disabled, Metadata: meta,
	}
	switch row.Scope {
	case "system_agent":
		params.AgentID = row.AgentID
	case "user":
		params.UserID = row.UserID
	case "user_agent":
		params.UserID = row.UserID
		params.AgentID = row.AgentID
	}
	return s.q.UpdateSkillMetadata(ctx, params)
}

func (s *testSkillStore) UpsertFile(ctx context.Context, skillID, path, content string) error {
	return s.q.UpsertSkillFile(ctx, sqlc.UpsertSkillFileParams{SkillID: skillID, Path: path, Content: content})
}

func (s *testSkillStore) DeleteFile(ctx context.Context, skillID, path string) error {
	return s.q.DeleteSkillFile(ctx, sqlc.DeleteSkillFileParams{SkillID: skillID, Path: path})
}

func (s *testSkillStore) Delete(ctx context.Context, id string) error {
	row, err := s.q.GetSkill(ctx, sqlc.GetSkillParams{ID: id, AgentID: pgtype.Text{}, UserID: pgtype.Text{}})
	if err != nil {
		return err
	}
	params := sqlc.DeleteSkillParams{ID: id}
	switch row.Scope {
	case "system_agent":
		params.AgentID = row.AgentID
	case "user":
		params.UserID = row.UserID
	case "user_agent":
		params.UserID = row.UserID
		params.AgentID = row.AgentID
	}
	return s.q.DeleteSkill(ctx, params)
}

// recordingSkillStore proves remove delegates lifecycle ownership to the
// adapter Delete method instead of mutating status through Update.
type recordingSkillStore struct {
	*testSkillStore
	deleteCalls int
	updateCalls int
}

func (s *recordingSkillStore) Delete(ctx context.Context, id string) error {
	s.deleteCalls++
	return nil
}

func (s *recordingSkillStore) Update(ctx context.Context, id string, patch pkgplugins.SkillUpdatePatch) error {
	s.updateCalls++
	return nil
}

func (s *testSkillStore) TouchReflectSkillRuntimeUse(ctx context.Context, skillID string, userID string, agentID string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE skill_usage
		SET use_count = use_count + 1,
		    last_used_at = now()
		WHERE skill_id = $1
		  AND user_id = $2
		  AND agent_id = $3
	`, skillID, userID, agentID)
	return err
}

func TestCreateIgnoresLegacyKnowledgeType(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), userID), agentID)

	if _, err := tool.Execute(ctx, map[string]any{
		"action":         "create",
		"name":           "deployment-notes",
		"description":    "Reusable deployment procedure",
		"content":        "# deployment-notes\n",
		"knowledge_type": "fact",
	}); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	sk, err := store.Resolve(ctx, "deployment-notes", pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("resolve created skill: %v", err)
	}
	if sk == nil {
		t.Fatal("created skill not found")
	}
	if sk.DisableModelInvocation {
		t.Fatal("legacy knowledge_type must not create a hidden knowledge skill")
	}
	var meta map[string]any
	if err := json.Unmarshal(sk.Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if _, ok := meta["knowledge_type"]; ok {
		t.Fatalf("skill metadata must not preserve legacy knowledge_type: %s", sk.Metadata)
	}
}

func tsMapRow(r sqlc.Skill) pkgplugins.Skill {
	meta := json.RawMessage("{}")
	if len(r.Metadata) != 0 {
		meta = r.Metadata
	}
	return pkgplugins.Skill{
		ID:                     r.ID,
		Scope:                  r.Scope,
		UserID:                 r.UserID.String,
		AgentID:                r.AgentID.String,
		Name:                   r.Name,
		Description:            r.Description,
		Status:                 r.Status,
		DisableModelInvocation: r.DisableModelInvocation,
		Metadata:               meta,
	}
}

// ctxWithUser returns a context carrying userID and agentID.
func ctxWithUser(userID string, agentID string) context.Context {
	ctx := context.Background()
	ctx = authz.WithUserID(ctx, userID)
	ctx = authz.WithAgentID(ctx, agentID)
	return ctx
}

func TestDefinition(t *testing.T) {
	tool := NewTool(nil, "/tmp/stella", "")
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
	tool := NewTool(nil, "/tmp/stella", "")
	_, err := tool.Execute(context.Background(), map[string]any{"action": "bogus"})
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestExecuteDispatch(t *testing.T) {
	tool := NewTool(nil, "/tmp/stella", "")
	_, err := tool.Execute(context.Background(), map[string]any{"action": "search"})
	if err == nil {
		t.Error("expected error for search without query")
	}
	_, err = tool.Execute(context.Background(), map[string]any{"action": "search_installed"})
	if err == nil {
		t.Error("expected error for search_installed without query")
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
	tool := NewTool(nil, "/tmp/stella", "")
	_, err := tool.search(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing query")
	}
}

func TestSearchInstalledRanksVisibleSkills(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "deploy-runbook",
		Description: "Reusable release checklist for production rollout",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# Deploy Runbook"}); err != nil {
		t.Fatalf("create deploy skill: %v", err)
	}
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "meeting-notes",
		Description: "Capture team discussion notes",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# Meeting Notes"}); err != nil {
		t.Fatalf("create notes skill: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	out, err := tool.Execute(ctx, map[string]any{
		"action": "search_installed",
		"query":  "release checklist",
	})
	if err != nil {
		t.Fatalf("search_installed: %v", err)
	}

	var results []struct {
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		Scope         string   `json:"scope"`
		MatchedFields []string `json:"matched_fields"`
		Score         float64  `json:"score"`
		Snippet       string   `json:"snippet"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("unmarshal search_installed results: %v\n%s", err, out)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one match", results)
	}
	if results[0].Name != "deploy-runbook" {
		t.Fatalf("top result = %q, want deploy-runbook", results[0].Name)
	}
	if results[0].Description == "" || results[0].Scope != "user" || results[0].Score <= 0 || results[0].Snippet == "" {
		t.Fatalf("result missing compact metadata: %#v", results[0])
	}
}

func TestSearchInstalledBoostsExactNameBeforeLimit(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	for i := range 12 {
		if _, err := store.Create(ctx, pkgplugins.Skill{
			Scope:       "user",
			UserID:      userID,
			Name:        fmt.Sprintf("strong-description-%02d", i),
			Description: "target skill target skill target skill target skill target skill",
			Status:      "active",
		}, map[string]string{pkgplugins.SkillMainFile: "# Strong"}); err != nil {
			t.Fatalf("create strong skill %d: %v", i, err)
		}
	}
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "target-skill",
		Description: "Generic helper",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# Target"}); err != nil {
		t.Fatalf("create target skill: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	out, err := tool.Execute(ctx, map[string]any{
		"action": "search_installed",
		"query":  "target-skill",
		"limit":  1,
	})
	if err != nil {
		t.Fatalf("search_installed: %v", err)
	}

	var results []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("unmarshal search_installed results: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Name != "target-skill" {
		t.Fatalf("results = %#v, want exact name match before truncation", results)
	}
}

func TestSearchInstalledAcceptsIntLimit(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	for i := range 2 {
		if _, err := store.Create(ctx, pkgplugins.Skill{
			Scope:       "user",
			UserID:      userID,
			Name:        fmt.Sprintf("deploy-%d", i),
			Description: "release checklist",
			Status:      "active",
		}, map[string]string{pkgplugins.SkillMainFile: "# Deploy"}); err != nil {
			t.Fatalf("create deploy skill %d: %v", i, err)
		}
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	out, err := tool.Execute(ctx, map[string]any{
		"action": "search_installed",
		"query":  "release checklist",
		"limit":  1,
	})
	if err != nil {
		t.Fatalf("search_installed: %v", err)
	}

	var results []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("unmarshal search_installed results: %v\n%s", err, out)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want int limit respected; results=%#v", len(results), results)
	}
}

func TestSearchInstalledDoesNotSearchSkillBody(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "body-only",
		Description: "Generic helper",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# Body\nsecretbodytoken"}); err != nil {
		t.Fatalf("create body-only skill: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	out, err := tool.Execute(ctx, map[string]any{
		"action": "search_installed",
		"query":  "secretbodytoken",
	})
	if err != nil {
		t.Fatalf("search_installed: %v", err)
	}
	if out != "No installed skills found." {
		t.Fatalf("search_installed searched body content, got: %s", out)
	}
}

func TestInstallMissingSource(t *testing.T) {
	tool := NewTool(nil, "/tmp/stella", "")
	_, err := tool.install(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing source")
	}
}

func TestRemoveMissingName(t *testing.T) {
	tool := NewTool(nil, "/tmp/stella", "")
	_, err := tool.remove(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestRemoveInvalidName(t *testing.T) {
	tool := NewTool(nil, "/tmp/stella", "")
	_, err := tool.remove(context.Background(), map[string]any{"name": "../../../etc"})
	if err == nil {
		t.Error("expected error for path traversal name")
	}
}

func TestTargetScopeDefaultsToUser(t *testing.T) {
	store, _, _ := newTestSkillStore(t)

	tool := NewTool(store, "/tmp/stella", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	scope, err := tool.targetScope(ctxWithUser("7", "agent-1"), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scope != skillScopeUser {
		t.Fatalf("scope = %q, want %q", scope, skillScopeUser)
	}
}

func TestTargetScopeRequiresUserContext(t *testing.T) {
	tool := NewTool(nil, "/tmp/stella", "")
	if _, err := tool.targetScope(context.Background(), ""); err == nil {
		t.Fatal("expected error for user scope without a user in context")
	}
}

// A group turn carries an agent but no user (D9 keeps identity as the group):
// user-scope writes are refused (no owner), while the agent scope still works.
func TestTargetScopeGroupContext(t *testing.T) {
	tool := NewTool(nil, "/tmp/stella", "")
	groupCtx := authz.WithAgentID(context.Background(), "agent-1")

	if _, err := tool.targetScope(groupCtx, "user"); err == nil {
		t.Fatal("expected user scope to be refused in a group (no user) context")
	}
	if scope, err := tool.targetScope(groupCtx, "agent"); err != nil || scope != skillScopeAgent {
		t.Fatalf("agent scope in group context = (%q, %v), want (%q, nil)", scope, err, skillScopeAgent)
	}
}

func TestInstallProjectScopeIsRejected(t *testing.T) {
	store, _, _ := newTestSkillStore(t)

	tool := NewTool(store, "/tmp/stella", t.TempDir()).WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	_, err := tool.install(context.Background(), map[string]any{
		"source": t.TempDir(),
		"scope":  "project",
	})
	if err == nil {
		t.Fatal("expected error when project scope is requested")
	}
	if !strings.Contains(err.Error(), "scope=project is not supported") {
		t.Fatalf("expected project scope rejection error, got %v", err)
	}
}

func TestInstallRejectsNonStringScope(t *testing.T) {
	// scope parse happens before store check
	tool := NewTool(nil, "/tmp/stella", t.TempDir())
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

// TestToolWriteAuthorizationEnforced proves the reflect reviewer tool's
// create/patch/deprecate are each authorized against ResourceSkill before the
// store mutation: a denial rejects the write (and the store is untouched), an
// allowed actor succeeds, and a missing authorizer fails closed.
func TestToolWriteAuthorizationEnforced(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user", UserID: userID, Name: "existing", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "---\nname: existing\n---\nbody"}); err != nil {
		t.Fatalf("seed skill: %v", err)
	}

	t.Run("denied create is rejected and not stored", func(t *testing.T) {
		calls := 0
		tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(denySkillWrites{calls: &calls})
		if _, err := tool.create(ctx, map[string]any{"name": "new-skill", "description": "a new skill"}); err == nil {
			t.Fatal("expected denied create to fail")
		}
		if calls == 0 {
			t.Fatal("write PEP was not consulted on create")
		}
		if sk, _ := store.Resolve(ctx, "new-skill", pkgplugins.SkillViewContext{UserID: userID}); sk != nil {
			t.Fatal("denied create still wrote the skill to the store")
		}
	})

	t.Run("denied patch and deprecate are rejected", func(t *testing.T) {
		calls := 0
		tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(denySkillWrites{calls: &calls})
		if _, err := tool.patch(ctx, map[string]any{"name": "existing", "description": "changed"}); err == nil {
			t.Fatal("expected denied patch to fail")
		}
		if _, err := tool.deprecate(ctx, map[string]any{"name": "existing"}); err == nil {
			t.Fatal("expected denied deprecate to fail")
		}
		if calls != 2 {
			t.Fatalf("write PEP consulted %d times, want 2 patch/deprecate authorizations", calls)
		}
	})

	t.Run("allowed create/patch/deprecate succeed", func(t *testing.T) {
		tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
		if _, err := tool.create(ctx, map[string]any{"name": "ok-skill", "description": "an allowed skill"}); err != nil {
			t.Fatalf("allowed create: %v", err)
		}
		if _, err := tool.patch(ctx, map[string]any{"name": "ok-skill", "description": "updated"}); err != nil {
			t.Fatalf("allowed patch: %v", err)
		}
		if _, err := tool.deprecate(ctx, map[string]any{"name": "ok-skill"}); err != nil {
			t.Fatalf("allowed deprecate: %v", err)
		}
	})

	t.Run("no write authorizer fails closed", func(t *testing.T) {
		tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{})
		if _, err := tool.create(ctx, map[string]any{"name": "nope", "description": "d"}); err == nil {
			t.Fatal("expected nil write authorizer to fail closed on create")
		}
		if _, err := tool.patch(ctx, map[string]any{"name": "existing", "description": "d"}); err == nil {
			t.Fatal("expected nil write authorizer to fail closed on patch")
		}
	})
}

// TestToolInstallRemoveWriteAuthorizationEnforced proves install (create) and
// remove (write) enforce the ResourceSkill write PEP internally even though the
// model-facing tool never exposes them (actionsOnly): a denial rejects the
// mutation and leaves the store untouched, an allowed actor succeeds, and a
// missing authorizer fails closed.
func TestToolInstallRemoveWriteAuthorizationEnforced(t *testing.T) {
	srcDir := t.TempDir()
	skillDir := filepath.Join(srcDir, "install-me")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: install-me\ndescription: install target\nstatus: active\n---\n# Install Me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	installArgs := func() map[string]any { return map[string]any{"source": srcDir} }

	t.Run("denied install is rejected and not stored", func(t *testing.T) {
		calls := 0
		tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(denySkillWrites{calls: &calls})
		if _, err := tool.install(ctx, installArgs()); err == nil {
			t.Fatal("expected denied install to fail")
		}
		if calls == 0 {
			t.Fatal("write PEP was not consulted on install")
		}
		if sk, _ := store.Resolve(ctx, "install-me", pkgplugins.SkillViewContext{UserID: userID}); sk != nil {
			t.Fatal("denied install still wrote the skill to the store")
		}
	})

	t.Run("no write authorizer fails closed on install", func(t *testing.T) {
		tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{})
		if _, err := tool.install(ctx, installArgs()); err == nil {
			t.Fatal("expected nil write authorizer to fail closed on install")
		}
		if sk, _ := store.Resolve(ctx, "install-me", pkgplugins.SkillViewContext{UserID: userID}); sk != nil {
			t.Fatal("nil-authorizer install still wrote the skill to the store")
		}
	})

	t.Run("allowed install then denied remove keeps the skill", func(t *testing.T) {
		tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
		if _, err := tool.install(ctx, installArgs()); err != nil {
			t.Fatalf("allowed install: %v", err)
		}

		calls := 0
		denyTool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(denySkillWrites{calls: &calls})
		if _, err := denyTool.remove(ctx, map[string]any{"name": "install-me"}); err == nil {
			t.Fatal("expected denied remove to fail")
		}
		if calls == 0 {
			t.Fatal("write PEP was not consulted on remove")
		}
		if sk, _ := store.Resolve(ctx, "install-me", pkgplugins.SkillViewContext{UserID: userID}); sk == nil {
			t.Fatal("denied remove still deleted the skill")
		}

		nilTool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{})
		if _, err := nilTool.remove(ctx, map[string]any{"name": "install-me"}); err == nil {
			t.Fatal("expected nil write authorizer to fail closed on remove")
		}
		if sk, _ := store.Resolve(ctx, "install-me", pkgplugins.SkillViewContext{UserID: userID}); sk == nil {
			t.Fatal("nil-authorizer remove still deleted the skill")
		}

		allowTool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
		if _, err := allowTool.remove(ctx, map[string]any{"name": "install-me"}); err != nil {
			t.Fatalf("allowed remove: %v", err)
		}
		if sk, _ := store.Resolve(ctx, "install-me", pkgplugins.SkillViewContext{UserID: userID}); sk != nil {
			t.Fatal("allowed remove did not delete the skill")
		}
	})
}

// TestToolReadAuthorizationEnforced proves the ResourceSkill read PEP gates every
// DB-backed skill read: a denied actor (custom deny / revoked grant) cannot load
// or see a DB skill, an unexpected authorization failure propagates, and no
// injected authorizer fails closed.
func TestToolReadAuthorizationEnforced(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user", UserID: userID, AgentID: agentID, Name: "secret-skill",
		Description: "a private db skill", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "---\nname: secret-skill\n---\nbody"}); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	t.Run("denied load is not-found", func(t *testing.T) {
		calls := 0
		tool := NewTool(store, "", "").WithReadAuthorizer(denySkillReads{calls: &calls})
		if _, err := tool.load(ctx, map[string]any{"name": "secret-skill"}); err == nil {
			t.Fatal("expected denied DB skill load to fail")
		}
		if calls == 0 {
			t.Fatal("read PEP was not consulted on load")
		}
	})

	t.Run("denied list drops the db skill", func(t *testing.T) {
		tool := NewTool(store, "", "").WithReadAuthorizer(denySkillReads{})
		out, err := tool.list(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if strings.Contains(out, "secret-skill") {
			t.Fatalf("denied db skill leaked into list: %s", out)
		}
	})

	t.Run("denied search drops the db skill", func(t *testing.T) {
		tool := NewTool(store, "", "").WithReadAuthorizer(denySkillReads{})
		out, err := tool.searchInstalled(ctx, map[string]any{"query": "secret"})
		if err != nil {
			t.Fatalf("search_installed: %v", err)
		}
		if strings.Contains(out, "secret-skill") {
			t.Fatalf("denied db skill leaked into search: %s", out)
		}
	})

	t.Run("authorization error propagates", func(t *testing.T) {
		tool := NewTool(store, "", "").WithReadAuthorizer(erroringSkillReads{})
		if _, err := tool.load(ctx, map[string]any{"name": "secret-skill"}); err == nil {
			t.Fatal("expected read authorization error to propagate on load")
		}
		if _, err := tool.list(ctx); err == nil {
			t.Fatal("expected read authorization error to propagate on list")
		}
	})

	t.Run("no authorizer fails closed", func(t *testing.T) {
		tool := NewTool(store, "", "")
		if _, err := tool.load(ctx, map[string]any{"name": "secret-skill"}); err == nil {
			t.Fatal("expected nil authorizer to fail closed on load")
		}
		out, err := tool.list(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if strings.Contains(out, "secret-skill") {
			t.Fatalf("nil authorizer leaked db skill: %s", out)
		}
	})
}

func TestLoadViaStore(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		AgentID:     agentID,
		Name:        "test-skill",
		Description: "A test skill",
		Status:      "active",
	}, map[string]string{
		pkgplugins.SkillMainFile: "---\nname: test-skill\ndescription: A test skill\n---\n# Test Skill\nDo the thing.",
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})

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
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	skillID, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		AgentID:     agentID,
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

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
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

func TestLoadReflectOwnedUserAgentSkillTouchesRuntimeUsage(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)
	metadata, err := MarkReflectOwnedMetadata(nil)
	if err != nil {
		t.Fatalf("reflect metadata: %v", err)
	}

	skillID, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user_agent",
		UserID:      userID,
		AgentID:     agentID,
		Name:        "reflect-runtime-skill",
		Description: "Reflect-owned runtime skill",
		Status:      "active",
		Metadata:    metadata,
	}, map[string]string{
		pkgplugins.SkillMainFile: "# Reflect Runtime Skill",
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}
	seedSkillUsage(t, store, skillID, userID, agentID, 2, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	if _, err := tool.load(ctx, map[string]any{"name": "reflect-runtime-skill"}); err != nil {
		t.Fatalf("load skill: %v", err)
	}

	useCount, lastUsed := loadSkillUsage(t, store, skillID)
	if useCount != 3 {
		t.Fatalf("use_count = %d, want 3", useCount)
	}
	if !lastUsed.After(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("last_used_at = %s, want refreshed timestamp", lastUsed)
	}
}

type blockingSkillUsageStore struct {
	*testSkillStore
	deadline time.Time
	resolves int
}

func (s *blockingSkillUsageStore) Resolve(ctx context.Context, name string, vc pkgplugins.SkillViewContext) (*pkgplugins.Skill, error) {
	s.resolves++
	return s.testSkillStore.Resolve(ctx, name, vc)
}

func (s *blockingSkillUsageStore) TouchReflectSkillRuntimeUse(ctx context.Context, _ string, _ string, _ string) error {
	s.deadline, _ = ctx.Deadline()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(1200 * time.Millisecond):
		return nil
	}
}

func TestLoadReflectOwnedSkillBoundsUsageLatencyAndResolvesOnce(t *testing.T) {
	base, userID, agentID := newTestSkillStore(t)
	store := &blockingSkillUsageStore{testSkillStore: base}
	ctx := ctxWithUser(userID, agentID)
	metadata, err := MarkReflectOwnedMetadata(nil)
	if err != nil {
		t.Fatalf("reflect metadata: %v", err)
	}
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user_agent", UserID: userID, AgentID: agentID,
		Name: "reflect-runtime-timeout", Description: "bounded usage touch",
		Status: "active", Metadata: metadata,
	}, map[string]string{pkgplugins.SkillMainFile: "# Runtime Timeout"}); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	started := time.Now()
	out, err := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{}).load(ctx, map[string]any{"name": "reflect-runtime-timeout"})
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	if !strings.Contains(out, "# Runtime Timeout") {
		t.Fatalf("main load result lost after usage timeout: %s", out)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("skill load blocked for %s, want bounded best-effort touch", elapsed)
	}
	if store.deadline.IsZero() {
		t.Fatal("usage tracker did not receive a deadline")
	}
	if store.resolves != 1 {
		t.Fatalf("Resolve calls = %d, want one resolution shared by load and touch", store.resolves)
	}
}

func TestLoadNonReflectSkillDoesNotTouchRuntimeUsage(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	skillID, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user_agent",
		UserID:      userID,
		AgentID:     agentID,
		Name:        "manual-runtime-skill",
		Description: "Manual runtime skill",
		Status:      "active",
	}, map[string]string{
		pkgplugins.SkillMainFile: "# Manual Runtime Skill",
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}
	seededAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	seedSkillUsage(t, store, skillID, userID, agentID, 2, seededAt)

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	if _, err := tool.load(ctx, map[string]any{"name": "manual-runtime-skill"}); err != nil {
		t.Fatalf("load skill: %v", err)
	}

	useCount, lastUsed := loadSkillUsage(t, store, skillID)
	if useCount != 2 {
		t.Fatalf("use_count = %d, want unchanged 2", useCount)
	}
	if !lastUsed.Equal(seededAt) {
		t.Fatalf("last_used_at = %s, want unchanged %s", lastUsed, seededAt)
	}
}

func TestLoadMaterializesDBSkillDir(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	skillID, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "system_agent",
		AgentID:     agentID,
		Name:        "agent-db-skill",
		Description: "DB-backed agent skill",
		Status:      "active",
	}, map[string]string{
		pkgplugins.SkillMainFile: "# Agent DB Skill",
		"scripts/run.sh":         "#!/bin/sh\necho ok\n",
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	agentBase := t.TempDir()
	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{}).WithSkillDiskLayout(SkillDiskLayout{Agent: agentBase})
	result, err := tool.load(ctx, map[string]any{"name": "agent-db-skill"})
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	wantDir := filepath.Join(agentBase, "agent-db-skill")
	if !strings.Contains(result, "<skill_dir>"+wantDir+"</skill_dir>") {
		t.Fatalf("skill_dir not emitted for materialized dir: %q", result)
	}
	if got, err := os.ReadFile(filepath.Join(wantDir, "scripts", "run.sh")); err != nil || string(got) != "#!/bin/sh\necho ok\n" {
		t.Fatalf("materialized script = %q, %v", got, err)
	}

	stale := filepath.Join(wantDir, "stale.md")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	if err := tool.materializeDBSkill(ctx, "", ""); err != nil {
		t.Fatalf("empty materialize should be a no-op: %v", err)
	}
	if err := tool.materializeDBSkill(ctx, skillID, wantDir); err != nil {
		t.Fatalf("materialize skill: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file exists after materialize: %v", err)
	}
}

func seedSkillUsage(t *testing.T, store *testSkillStore, skillID string, userID string, agentID string, useCount int64, lastUsedAt time.Time) {
	t.Helper()
	if _, err := store.db.Exec(context.Background(), `
		INSERT INTO skill_usage (skill_id, user_id, agent_id, use_count, last_used_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $5)
	`, skillID, userID, agentID, useCount, lastUsedAt); err != nil {
		t.Fatalf("seed skill_usage: %v", err)
	}
}

func loadSkillUsage(t *testing.T, store *testSkillStore, skillID string) (int64, time.Time) {
	t.Helper()
	var useCount int64
	var lastUsedAt time.Time
	if err := store.db.QueryRow(context.Background(), `
		SELECT use_count, last_used_at
		FROM skill_usage
		WHERE skill_id = $1
	`, skillID).Scan(&useCount, &lastUsedAt); err != nil {
		t.Fatalf("load skill_usage: %v", err)
	}
	return useCount, lastUsedAt
}

func TestLoadRefreshesStaleDBSkillDir(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	skillID, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "system_agent",
		AgentID:     agentID,
		Name:        "refresh-db-skill",
		Description: "DB-backed agent skill",
		Status:      "active",
	}, map[string]string{
		pkgplugins.SkillMainFile: "# Refresh DB Skill",
		"scripts/run.sh":         "#!/bin/sh\necho old\n",
		"notes/stale.md":         "remove me\n",
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	agentBase := t.TempDir()
	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{}).WithSkillDiskLayout(SkillDiskLayout{Agent: agentBase})
	result, err := tool.load(ctx, map[string]any{"name": "refresh-db-skill"})
	if err != nil {
		t.Fatalf("first load skill: %v", err)
	}
	wantDir := filepath.Join(agentBase, "refresh-db-skill")
	if !strings.Contains(result, "<skill_dir>"+wantDir+"</skill_dir>") {
		t.Fatalf("skill_dir not emitted for materialized dir: %q", result)
	}
	if got, err := os.ReadFile(filepath.Join(wantDir, "scripts", "run.sh")); err != nil || string(got) != "#!/bin/sh\necho old\n" {
		t.Fatalf("initial script = %q, %v", got, err)
	}

	if err := store.UpsertFile(ctx, skillID, "scripts/run.sh", "#!/bin/sh\necho fresh\n"); err != nil {
		t.Fatalf("update script: %v", err)
	}
	if err := store.DeleteFile(ctx, skillID, "notes/stale.md"); err != nil {
		t.Fatalf("delete stale file: %v", err)
	}

	result, err = tool.load(ctx, map[string]any{"name": "refresh-db-skill"})
	if err != nil {
		t.Fatalf("second load skill: %v", err)
	}
	if !strings.Contains(result, "<skill_dir>"+wantDir+"</skill_dir>") {
		t.Fatalf("skill_dir not emitted after refresh: %q", result)
	}
	if got, err := os.ReadFile(filepath.Join(wantDir, "scripts", "run.sh")); err != nil || string(got) != "#!/bin/sh\necho fresh\n" {
		t.Fatalf("refreshed script = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "notes", "stale.md")); !os.IsNotExist(err) {
		t.Fatalf("deleted DB file exists after refresh: %v", err)
	}
}

func TestLoadOmitsSkillDirWhenMaterializeFails(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "system_agent",
		AgentID:     agentID,
		Name:        "unwritable-skill",
		Description: "DB-backed agent skill",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# Still Loads"})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	agentBase := t.TempDir()
	blockingFile := filepath.Join(agentBase, "unwritable-skill")
	if err := os.WriteFile(blockingFile, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{}).WithSkillDiskLayout(SkillDiskLayout{Agent: agentBase})
	result, err := tool.load(ctx, map[string]any{"name": "unwritable-skill"})
	if err != nil {
		t.Fatalf("load should return DB content when materialization fails: %v", err)
	}
	if strings.Contains(result, "<skill_dir>") {
		t.Fatalf("unexpected skill_dir when materialization fails: %q", result)
	}
	if !strings.Contains(result, "# Still Loads") {
		t.Fatalf("missing DB skill content: %q", result)
	}
}

func TestListViaStore(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		AgentID:     agentID,
		Name:        "my-skill",
		Description: "My skill",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# My Skill"})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
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
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		AgentID:     agentID,
		Name:        "removable-skill",
		Description: "Removable",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# Removable"})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
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

func TestToolRemoveUsesAdapterDelete(t *testing.T) {
	base, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)
	if _, err := base.Create(ctx, pkgplugins.Skill{
		Scope: "user", UserID: userID, Name: "tool-remove-lifecycle", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# lifecycle"}); err != nil {
		t.Fatalf("create skill: %v", err)
	}
	store := &recordingSkillStore{testSkillStore: base}
	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})

	if _, err := tool.Execute(ctx, map[string]any{"action": "remove", "name": "tool-remove-lifecycle"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if store.deleteCalls != 1 || store.updateCalls != 0 {
		t.Fatalf("tool lifecycle calls = delete %d, update %d; want delete 1, update 0", store.deleteCalls, store.updateCalls)
	}
}

// TestRemoveRespectsScope asserts that when a user-global and a per-agent skill
// share the same name, remove honors args["scope"] instead of silently deleting
// whichever Resolve returns first (user_agent, by precedence). The model-facing
// "agent" scope is the user's per-agent (user_agent) skill.
func TestRemoveRespectsScope(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user", UserID: userID, Name: "dup", Description: "u", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# u"}); err != nil {
		t.Fatalf("create user skill: %v", err)
	}
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "dup", Description: "a", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# a"}); err != nil {
		t.Fatalf("create user_agent skill: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})

	// scope=agent must delete the per-agent (user_agent) row, leaving user alive.
	if _, err := tool.remove(ctx, map[string]any{"name": "dup", "scope": "agent"}); err != nil {
		t.Fatalf("remove agent scope: %v", err)
	}

	list, err := store.List(ctx, pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var userLeft, agentLeft bool
	for _, s := range list {
		if s.Name != "dup" {
			continue
		}
		switch s.Scope {
		case "user":
			userLeft = true
		case "user_agent":
			agentLeft = true
		}
	}
	if !userLeft {
		t.Error("expected user-scoped dup to remain")
	}
	if agentLeft {
		t.Error("expected user_agent-scoped dup to be deleted")
	}
}

// TestRemoveScopeNotFound returns a specific error when an explicit scope has no match.
func TestRemoveScopeNotFound(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user", UserID: userID, Name: "only-user", Description: "u", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# u"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	_, err := tool.remove(ctx, map[string]any{"name": "only-user", "scope": "agent"})
	if err == nil {
		t.Fatal("expected error when scope has no match")
	}
	if !strings.Contains(err.Error(), "scope=agent") {
		t.Errorf("expected scope in error message, got %q", err.Error())
	}
}

// TestRemoveRejectsAdminScope asserts the model-facing tool cannot remove an
// admin-managed system_agent skill even when it is the effective resolution.
func TestRemoveRejectsAdminScope(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "system_agent", AgentID: agentID, Name: "shared", Description: "a", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# a"}); err != nil {
		t.Fatalf("create system_agent skill: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	if _, err := tool.remove(ctx, map[string]any{"name": "shared"}); err == nil {
		t.Fatal("expected error removing a system_agent skill via the tool")
	}
}

// TestPatchRespectsScope guards CR-007: patch honors an explicit scope instead
// of mutating whichever same-name skill Resolve returns by precedence.
func TestPatchRespectsScope(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user", UserID: userID, Name: "dup", Description: "u", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# u"}); err != nil {
		t.Fatalf("create user skill: %v", err)
	}
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "dup", Description: "a", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# a"}); err != nil {
		t.Fatalf("create user_agent skill: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	if _, err := tool.patch(ctx, map[string]any{"name": "dup", "scope": "user", "description": "changed-user"}); err != nil {
		t.Fatalf("patch user scope: %v", err)
	}

	userRows, err := store.ListByScope(ctx, "user", userID, "")
	if err != nil {
		t.Fatalf("list user scope: %v", err)
	}
	if len(userRows) != 1 || userRows[0].Description != "changed-user" {
		t.Fatalf("user dup = %#v, want changed description", userRows)
	}
	agentRows, err := store.ListByScope(ctx, "user_agent", userID, agentID)
	if err != nil {
		t.Fatalf("list user_agent scope: %v", err)
	}
	if len(agentRows) != 1 || agentRows[0].Status != "active" {
		t.Fatalf("user_agent dup = %#v, want still active", agentRows)
	}
}

// TestPatchDefaultScopeIsUser guards CR-007: omitted scope follows the tool
// contract instead of runtime precedence, and can update hidden knowledge rows.
func TestPatchDefaultScopeIsUser(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user", UserID: userID, Name: "dup", Description: "u", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# u"}); err != nil {
		t.Fatalf("create user skill: %v", err)
	}
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user_agent", UserID: userID, AgentID: agentID, Name: "dup", Description: "a", Status: "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# a"}); err != nil {
		t.Fatalf("create user_agent skill: %v", err)
	}
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope: "user", UserID: userID, Name: "fact", Description: "f", Status: "active", DisableModelInvocation: true,
	}, map[string]string{pkgplugins.SkillMainFile: "# f"}); err != nil {
		t.Fatalf("create disabled user skill: %v", err)
	}

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	if _, err := tool.patch(ctx, map[string]any{"name": "dup", "description": "changed-default"}); err != nil {
		t.Fatalf("patch default scope: %v", err)
	}
	if _, err := tool.patch(ctx, map[string]any{"name": "fact", "description": "changed-hidden"}); err != nil {
		t.Fatalf("patch hidden default-scope skill: %v", err)
	}

	userRows, err := store.ListByScope(ctx, "user", userID, "")
	if err != nil {
		t.Fatalf("list user scope: %v", err)
	}
	descriptions := map[string]string{}
	for _, row := range userRows {
		descriptions[row.Name] = row.Description
	}
	if descriptions["dup"] != "changed-default" || descriptions["fact"] != "changed-hidden" {
		t.Fatalf("user rows = %#v, want both descriptions updated", userRows)
	}
	agentRows, err := store.ListByScope(ctx, "user_agent", userID, agentID)
	if err != nil {
		t.Fatalf("list user_agent scope: %v", err)
	}
	if len(agentRows) != 1 || agentRows[0].Status != "active" {
		t.Fatalf("user_agent dup = %#v, want still active", agentRows)
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

	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
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

// makeProjectSkill writes a minimal SKILL.md into {root}/.agents/skills/{name}/.
func makeProjectSkill(t *testing.T, root, name, description string) {
	t.Helper()
	skillDir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\nstatus: active\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListMergesProjectSkills(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	projectRoot := t.TempDir()
	ctx := ctxWithUser(userID, agentID)

	// Create a DB user skill.
	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "db-skill",
		Description: "DB skill",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# DB Skill"})
	if err != nil {
		t.Fatalf("create db skill: %v", err)
	}

	// Create project skills on disk.
	makeProjectSkill(t, projectRoot, "proj-skill", "Project skill")
	// Create a project skill with same name as a DB skill to test shadowing.
	makeProjectSkill(t, projectRoot, "db-skill", "Project override of db-skill")

	tool := NewTool(store, "", projectRoot).WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	result, err := tool.list(WithProjectRoot(ctx, projectRoot))
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var skills []installedSkill
	if err := json.Unmarshal([]byte(result), &skills); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	// Verify db-skill appears with scope=project (project shadows DB).
	dbSkillFound := false
	projSkillFound := false
	for _, s := range skills {
		if s.Name == "db-skill" {
			dbSkillFound = true
			if s.Scope != "project" {
				t.Errorf("db-skill: expected scope=project (shadowed by project), got %q", s.Scope)
			}
			if s.Removable {
				t.Error("project skill should not be removable")
			}
		}
		if s.Name == "proj-skill" {
			projSkillFound = true
			if s.Scope != "project" {
				t.Errorf("proj-skill: expected scope=project, got %q", s.Scope)
			}
		}
	}
	if !dbSkillFound {
		t.Error("expected db-skill in list")
	}
	if !projSkillFound {
		t.Error("expected proj-skill in list")
	}
}

func TestLoadPrefersProjectSkill(t *testing.T) {
	store, userID, agentID := newTestSkillStore(t)
	projectRoot := t.TempDir()
	ctx := ctxWithUser(userID, agentID)

	// Create a DB user skill named "shared".
	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "shared",
		Description: "DB version",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# DB version"})
	if err != nil {
		t.Fatalf("create db skill: %v", err)
	}

	// Create a project skill with the same name.
	makeProjectSkill(t, projectRoot, "shared", "Project version")

	tool := NewTool(store, "", projectRoot).WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	result, err := tool.load(WithProjectRoot(ctx, projectRoot), map[string]any{"name": "shared"})
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	if !strings.Contains(result, "Project version") {
		t.Errorf("expected project version in result, got %q", result)
	}
}

func TestInstallAgentScope(t *testing.T) {
	srcDir := t.TempDir()
	skillDir := filepath.Join(srcDir, "agent-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: agent-skill\ndescription: Agent scope test\nstatus: active\n---\n# Agent Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, userID, agentID := newTestSkillStore(t)
	ctx := ctxWithUser(userID, agentID)

	tool := NewTool(store, "", "").WithReadAuthorizer(allowAllSkillReads{}).WithWriteAuthorizer(allowAllSkillWrites{})
	result, err := tool.install(ctx, map[string]any{"source": srcDir, "scope": "agent"})
	if err != nil {
		t.Fatalf("install error: %v", err)
	}
	if !strings.Contains(result, "scope=user_agent") {
		t.Fatalf("expected user_agent scope in result, got %q", result)
	}

	// The model-facing "agent" scope persists as user_agent: the user's own
	// skill bound to this agent, not the admin-managed system_agent scope.
	vc := pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID}
	skills, err := store.List(ctx, vc)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, s := range skills {
		if s.Name == "agent-skill" {
			found = true
			if s.Scope != "user_agent" {
				t.Errorf("expected scope 'user_agent', got %q", s.Scope)
			}
			if s.UserID != userID {
				t.Errorf("expected user_id %q, got %q", userID, s.UserID)
			}
			if s.AgentID != agentID {
				t.Errorf("expected agent_id %q, got %q", agentID, s.AgentID)
			}
		}
	}
	if !found {
		t.Error("expected agent-skill in store after install")
	}
}
