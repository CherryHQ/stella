package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/home"
	serverpkg "github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/internal/skillaccess"
	"github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// setupHomeAuthorityHTTPServer reuses the standard authenticated HTTP fixture,
// replacing only its Skill authority with the exact production Home composition.
// Its local Home directory is explicit; it never observes ~/.stella.
func setupHomeAuthorityHTTPServer(t *testing.T) (*testEnv, *skills.HomeAuthorityStore, *home.Registry) {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("STELLA_HOME", homeDir)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	env := setupAdmin(t)

	local, err := home.NewLocalStore("local", homeDir)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(env.db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := sqlc.New(env.db)
	if _, err := q.CreateStorageMigrationObservation(ctx, sqlc.CreateStorageMigrationObservationParams{
		Name: home.MutableAssetObjectAuthorityMigration, State: "not_required", Metadata: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := skills.EnsureSkillHomeAuthority(ctx, env.db, registry); err != nil {
		t.Fatalf("ensure Home Skill authority: %v", err)
	}
	inventory, err := skills.NewStorageHomeCatalogInventory(q)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.NewHomeCatalog(registry, inventory)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := skills.NewHomeSkillPublisher(registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := skills.NewHomeSkillManager(catalog, publisher, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	homeStore, err := skills.NewHomeStore(catalog, manager)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := skills.NewHomeSkillUsageStore(env.db)
	if err != nil {
		t.Fatal(err)
	}
	reflectStore, err := skills.NewHomeReflectStore(homeStore, usage)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := skills.NewHomeAuthorityStore(homeStore, reflectStore)
	if err != nil {
		t.Fatal(err)
	}

	env.pluginHost.SetSkillStore(authority)
	env.deps.PluginHost = env.pluginHost
	env.deps.SkillAccess = skillaccess.NewService(authority, env.deps.AgentAccess)
	env.srv, err = serverpkg.New(context.Background(), env.deps)
	if err != nil {
		t.Fatalf("rebuild HTTP server with Home Skill authority: %v", err)
	}
	return env, authority, registry
}

func newHomeSkillAuthorityForTest(t *testing.T, db *pgxpool.Pool) *skills.HomeAuthorityStore {
	t.Helper()
	local, err := home.NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.NewHomeCatalog(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := skills.NewHomeSkillPublisher(registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := skills.NewHomeSkillManager(catalog, publisher, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	homeStore, err := skills.NewHomeStore(catalog, manager)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := skills.NewHomeSkillUsageStore(db)
	if err != nil {
		t.Fatal(err)
	}
	reflectStore, err := skills.NewHomeReflectStore(homeStore, usage)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := skills.NewHomeAuthorityStore(homeStore, reflectStore)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func createHomeHTTPSkill(t *testing.T, store *skills.HomeAuthorityStore, scope, userID, agentID, name string, files map[string]string) skills.SkillSnapshot {
	t.Helper()
	snapshot, err := store.CreateManagedSkill(t.Context(), skills.Skill{
		Scope: scope, UserID: userID, AgentID: agentID, Name: name, Description: name,
	}, files)
	if err != nil {
		t.Fatalf("create Home skill %q: %v", name, err)
	}
	return snapshot
}

func homeSkillView(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(parseResponse(t, rr).Data, &value); err != nil {
		t.Fatalf("decode skill view: %v", err)
	}
	return value
}

func TestHomeSkillHTTPScopedCASAndOrdinaryRefusal(t *testing.T) {
	env, authority, registry := setupHomeAuthorityHTTPServer(t)
	main := "---\nname: managed\ndescription: managed\n---\ninitial\n"
	created := createHomeHTTPSkill(t, authority, "user", env.adminUser.ID, "", "managed", map[string]string{skills.MainFile: main, "note.txt": "note"})

	rr := doRequest(t, env, http.MethodGet, "/api/skills/"+created.Skill.ID, nil)
	if rr.Code != http.StatusOK || homeSkillView(t, rr)["content_digest"] != created.Skill.ContentDigest {
		t.Fatalf("GET managed Home Skill = %d %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodGet, "/api/skills?scope=user", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), created.Skill.ContentDigest) {
		t.Fatalf("list managed Home Skill = %d %s", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, env, http.MethodPatch, "/api/skills/"+created.Skill.ID, map[string]any{
		"expected_digest": created.Skill.ContentDigest, "files": map[string]string{"note.txt": "updated"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("exact scoped PATCH = %d %s", rr.Code, rr.Body.String())
	}
	updatedDigest, _ := homeSkillView(t, rr)["content_digest"].(string)
	if updatedDigest == "" || updatedDigest == created.Skill.ContentDigest {
		t.Fatalf("updated digest = %q", updatedDigest)
	}
	for _, body := range []map[string]any{
		{}, {"expected_digest": "bad"}, {"expected_digest": created.Skill.ContentDigest, "files": map[string]string{"note.txt": "stale"}},
	} {
		rr = doRequest(t, env, http.MethodPatch, "/api/skills/"+created.Skill.ID, body)
		want := http.StatusBadRequest
		if body["expected_digest"] == created.Skill.ContentDigest {
			want = http.StatusConflict
		}
		if rr.Code != want {
			t.Fatalf("scoped PATCH %#v = %d, want %d (%s)", body, rr.Code, want, rr.Body.String())
		}
	}
	content, err := authority.LoadFile(t.Context(), created.Skill.ID, "note.txt")
	if err != nil || content != "updated" {
		t.Fatalf("stale PATCH changed file: %q, %v", content, err)
	}

	base := "/api/skills/" + created.Skill.ID
	rr = doRequest(t, env, http.MethodDelete, base+"/file?path=note.txt", nil)
	if rr.Code != http.StatusBadRequest { // generated required-query binder
		t.Fatalf("file delete missing digest = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodDelete, base+"/file?path=note.txt&expected_digest="+created.Skill.ContentDigest, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("file delete stale digest = %d, want 409 (%s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodDelete, base+"/file?path=note.txt&expected_digest="+updatedDigest, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("file delete exact digest = %d, want 204 (%s)", rr.Code, rr.Body.String())
	}
	current, err := authority.Get(t.Context(), created.Skill.ID)
	if err != nil {
		t.Fatal(err)
	}
	rr = doRequest(t, env, http.MethodDelete, base, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("scoped delete missing digest = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodDelete, base+"?expected_digest="+updatedDigest, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("scoped delete stale digest = %d, want 409 (%s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodDelete, base+"?expected_digest="+current.ContentDigest, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("scoped delete exact digest = %d, want 204 (%s)", rr.Code, rr.Body.String())
	}

	root, err := home.UserSkillCatalog(env.adminUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.UseSkillFilesystem(t.Context(), root, func(filesystem sandbox.Filesystem) error {
		if err := filesystem.Mkdir(t.Context(), path.Join(sandbox.PathWorkspace, "ordinary"), 0o755); err != nil {
			return err
		}
		return filesystem.Write(t.Context(), path.Join(sandbox.PathWorkspace, "ordinary", skills.MainFile), strings.NewReader("---\nname: ordinary\ndescription: ordinary\n---\nunchanged\n"), sandbox.WriteOptions{})
	}); err != nil {
		t.Fatal(err)
	}
	ordinary, err := authority.Resolve(t.Context(), "ordinary", skills.ViewContext{UserID: env.adminUser.ID})
	if err != nil || ordinary.ContentDigest != "" {
		t.Fatalf("ordinary Home Skill = %#v, %v", ordinary, err)
	}
	rr = doRequest(t, env, http.MethodPatch, "/api/skills/"+ordinary.ID, map[string]any{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("ordinary missing digest = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodPatch, "/api/skills/"+ordinary.ID, map[string]any{"expected_digest": strings.Repeat("a", 64)})
	if rr.Code != http.StatusConflict {
		t.Fatalf("ordinary valid digest = %d, want 409 (%s)", rr.Code, rr.Body.String())
	}
	ordinaryContent, err := authority.LoadFile(t.Context(), ordinary.ID, skills.MainFile)
	if err != nil || !strings.Contains(ordinaryContent, "unchanged") {
		t.Fatalf("ordinary Home Skill changed: %q, %v", ordinaryContent, err)
	}
}

func TestHomeSkillHTTPAgentCASAndStaleUpgradeSkipsFetch(t *testing.T) {
	env, authority, _ := setupHomeAuthorityHTTPServer(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "home-cas-agent")
	created := createHomeHTTPSkill(t, authority, "user_agent", env.adminUser.ID, agentID, "agent-managed", map[string]string{
		skills.MainFile: "---\nname: agent-managed\ndescription: agent-managed\nsource: http://127.0.0.1/never\n---\nbody\n",
	})

	base := "/api/agents/" + agentID + "/skills/" + created.Skill.ID + "?scope=user_agent"
	rr := doRequest(t, env, http.MethodPatch, base, map[string]any{"description": "missing digest"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("agent PATCH missing digest = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodPatch, base, map[string]any{"expected_digest": created.Skill.ContentDigest, "description": "changed"})
	if rr.Code != http.StatusOK {
		t.Fatalf("agent exact PATCH = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	updatedDigest, _ := homeSkillView(t, rr)["content_digest"].(string)
	rr = doRequest(t, env, http.MethodDelete, base, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("agent DELETE missing digest = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodDelete, base+"&expected_digest="+updatedDigest, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("agent exact DELETE = %d, want 204 (%s)", rr.Code, rr.Body.String())
	}

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { upstreamCalls++ }))
	defer upstream.Close()
	metadata, err := json.Marshal(map[string]string{"source": upstream.URL + "/skill", "version": "old"})
	if err != nil {
		t.Fatal(err)
	}
	upgradable, err := authority.CreateManagedSkill(t.Context(), skills.Skill{
		Scope: "user_agent", UserID: env.adminUser.ID, AgentID: agentID, Name: "upgrade-managed", Description: "upgrade-managed", Metadata: metadata,
	}, map[string]string{skills.MainFile: "---\nname: upgrade-managed\ndescription: upgrade-managed\n---\nbody\n"})
	if err != nil {
		t.Fatal(err)
	}
	rr = doRequest(t, env, http.MethodPost, "/api/agents/"+agentID+"/skills/"+upgradable.Skill.ID+"/upgrade?scope=user_agent&expected_digest="+strings.Repeat("b", 64), nil)
	if rr.Code != http.StatusConflict || upstreamCalls != 0 {
		t.Fatalf("stale upgrade = %d calls=%d, want 409/0 (%s)", rr.Code, upstreamCalls, rr.Body.String())
	}

	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, skills.MainFile), []byte("---\nname: exact-upgrade\ndescription: fetched\n---\nfetched body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exactMetadata, err := json.Marshal(map[string]string{"source": sourceDir, "version": "old"})
	if err != nil {
		t.Fatal(err)
	}
	exact, err := authority.CreateManagedSkill(t.Context(), skills.Skill{
		Scope: "user_agent", UserID: env.adminUser.ID, AgentID: agentID, Name: "exact-upgrade", Description: "before", Metadata: exactMetadata,
	}, map[string]string{skills.MainFile: "---\nname: exact-upgrade\ndescription: before\n---\nbefore body\n"})
	if err != nil {
		t.Fatal(err)
	}
	rr = doRequest(t, env, http.MethodPost, "/api/agents/"+agentID+"/skills/"+exact.Skill.ID+"/upgrade?scope=user_agent&expected_digest="+exact.Skill.ContentDigest, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("exact upgrade = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	afterUpgrade, err := authority.Get(t.Context(), exact.Skill.ID)
	if err != nil || afterUpgrade.ContentDigest == exact.Skill.ContentDigest {
		t.Fatalf("exact upgrade did not commit Home CAS revision: %#v, %v", afterUpgrade, err)
	}
}

func TestHomeSkillHTTPProjectMutationNeedsNoDigest(t *testing.T) {
	env, _, _ := setupHomeAuthorityHTTPServer(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "project-skill-agent")
	projectRoot := t.TempDir()
	projectID, sessionID := uuid.NewString(), "project-skill-session"
	if _, err := env.db.Exec(t.Context(), `INSERT INTO project (id, agent_id, user_id, name, base_dir) VALUES ($1, $2, $3, $4, $5)`, projectID, agentID, env.adminUser.ID, "Project Skill", projectRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(t.Context(), `INSERT INTO ctx_conversation (session_id, title, channel, kind, project_id, agent_id, user_id) VALUES ($1, 'project', 'web', 'chat', $2, $3, $4)`, sessionID, projectID, agentID, env.adminUser.ID); err != nil {
		t.Fatal(err)
	}
	skillName := "skill-v2-demo"
	skillDir := filepath.Join(projectRoot, ".agents", "skills", skillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, skills.MainFile), []byte("---\nname: skill-v2-demo\ndescription: project\n---\nbefore\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	base := "/api/agents/" + agentID + "/skills/" + skillName + "?scope=project&session_id=" + sessionID
	rr := doRequest(t, env, http.MethodGet, base, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("project skill-v2-* GET = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodPatch, base, map[string]any{
		"files": map[string]string{skills.MainFile: "after\n"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("project PATCH without digest = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(skillDir, skills.MainFile))
	if err != nil || string(content) != "after\n" {
		t.Fatalf("project Skill content = %q, %v", content, err)
	}

	rr = doRequest(t, env, http.MethodGet, "/api/skills/skill-v2-invalid", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("malformed Home-looking scoped ID = %d, want opaque 404 (%s)", rr.Code, rr.Body.String())
	}
}
