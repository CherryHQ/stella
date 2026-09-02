package skillaccess

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/skills"
	storepkg "github.com/CherryHQ/stella/internal/store"
)

func TestEmbeddedPostgresSkillAccessMatrix(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := storepkg.NewDBStore(pool)
	oidc := appdb.NewOIDCStore(pool)
	assign := appdb.NewAuthStore(pool)
	skillStore := skillIdentityFixture{}

	owner, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "owner@sk.test", Name: "owner", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	other, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "other@sk.test", Name: "other", Role: auth.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	// A system-scoped agent so the folded agent-read gate always passes and the
	// tests isolate skill-row ownership boundaries.
	if err := store.CreateAgent(ctx, config.Agent{ID: "sys", Name: "sys", Model: "p/m", Workspace: "/tmp/sys", Scope: config.AgentScopeSystem, CreatorID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	userSkillID := mustCreateSkill(t, skillStore, skills.Skill{Scope: ScopeUser, UserID: owner.ID, Name: "user-skill"})
	userAgentSkillID := mustCreateSkill(t, skillStore, skills.Skill{Scope: ScopeUserAgent, UserID: owner.ID, AgentID: "sys", Name: "user-agent-skill"})
	systemSkillID := mustCreateSkill(t, skillStore, skills.Skill{Scope: ScopeSystem, Name: "system-skill"})
	systemAgentSkillID := mustCreateSkill(t, skillStore, skills.Skill{Scope: ScopeSystemAgent, AgentID: "sys", Name: "system-agent-skill"})

	svc := NewService(skillStore, agentaccess.NewService(store, assign))

	// Admin authorization ignores the specific id and no longer loads any agent
	// assignments, so the admin id need not parse as a uuid.
	adminID := uuid.NewString()

	userAuth := func(id string, admin bool) authz.Authority {
		a, err := authz.NewUserAuthority(authz.UserID(id), admin)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	agentAuth := func(userID, agentID string) authz.Authority {
		a, err := agentaccess.WorkerAgentAuthority(userID, agentID)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	// The by-id management read path escalates system reads to an admin operation,
	// so a non-admin user cannot read a system skill by id.
	readCases := []struct {
		name      string
		authority authz.Authority
		skillID   string
		wantErr   error
	}{
		{"owner reads own user skill", userAuth(owner.ID, false), userSkillID, nil},
		{"owner reads own user_agent skill", userAuth(owner.ID, false), userAgentSkillID, nil},
		{"foreign user cannot read user skill", userAuth(other.ID, false), userSkillID, ErrNotFound},
		{"foreign user cannot read user_agent skill", userAuth(other.ID, false), userAgentSkillID, ErrNotFound},
		{"admin reads any user skill", userAuth(adminID, true), userSkillID, nil},
		{"non-admin cannot read system skill by id", userAuth(other.ID, false), systemSkillID, ErrForbidden},
		{"admin reads system skill by id", userAuth(adminID, true), systemSkillID, nil},
		{"delegated executor reads own user_agent skill", agentAuth(owner.ID, "sys"), userAgentSkillID, nil},
		{"foreign delegated denied", agentAuth(other.ID, "sys"), userAgentSkillID, ErrNotFound},
	}
	for _, tc := range readCases {
		t.Run(tc.name, func(t *testing.T) {
			acc, err := svc.Begin(ctx, tc.authority)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			_, err = acc.AuthorizeManageByID(ctx, tc.skillID, authz.ActionRead)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("read = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("read = %v, want %v", err, tc.wantErr)
			}
		})
	}

	// AuthorizeRead is the tool/HTTP resolve/list read path: unlike the by-id
	// management path it does not escalate system reads to admin, so any user or
	// delegated agent may read shared system skills while owner rules still gate
	// user scopes.
	userSkill := skills.Skill{ID: userSkillID, Scope: ScopeUser, UserID: owner.ID}
	userAgentSkill := skills.Skill{ID: userAgentSkillID, Scope: ScopeUserAgent, UserID: owner.ID, AgentID: "sys"}
	systemSkill := skills.Skill{ID: systemSkillID, Scope: ScopeSystem}
	authorizeReadCases := []struct {
		name      string
		authority authz.Authority
		skill     skills.Skill
		wantErr   error
	}{
		{"owner reads own user skill", userAuth(owner.ID, false), userSkill, nil},
		{"owner reads own user_agent skill", userAuth(owner.ID, false), userAgentSkill, nil},
		{"foreign user cannot read user skill", userAuth(other.ID, false), userSkill, ErrNotFound},
		{"any user reads system skill", userAuth(other.ID, false), systemSkill, nil},
		{"delegated agent reads own user_agent skill", agentAuth(owner.ID, "sys"), userAgentSkill, nil},
		{"delegated agent reads user-scope skill", agentAuth(owner.ID, "sys"), userSkill, nil},
		{"delegated agent reads system skill", agentAuth(owner.ID, "sys"), systemSkill, nil},
		{"foreign delegated cannot read user_agent skill", agentAuth(other.ID, "sys"), userAgentSkill, ErrNotFound},
	}
	for _, tc := range authorizeReadCases {
		t.Run("read/"+tc.name, func(t *testing.T) {
			acc, err := svc.Begin(ctx, tc.authority)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			err = acc.AuthorizeRead(ctx, tc.skill)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("AuthorizeRead = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("AuthorizeRead = %v, want %v", err, tc.wantErr)
			}
		})
	}

	// A durable worker (reflect) write is authorized under a fresh reconstructed
	// WorkerAgentAuthority; owner+executor user_agent writes pass, a foreign
	// user/agent is denied.
	t.Run("worker write authorization", func(t *testing.T) {
		if err := svc.AuthorizeWorkerWrite(ctx, owner.ID, "sys", userAgentSkillID, false); err != nil {
			t.Fatalf("owner worker write = %v, want nil", err)
		}
		if err := svc.AuthorizeWorkerWrite(ctx, owner.ID, "sys", "", true); err != nil {
			t.Fatalf("owner worker create = %v, want nil", err)
		}
		if err := svc.AuthorizeWorkerWrite(ctx, other.ID, "sys", userAgentSkillID, false); err == nil {
			t.Fatal("foreign worker write should be denied")
		}
	})

	// Owner may write and delete own user/user_agent skills; a foreign user cannot.
	writeCases := []struct {
		name      string
		authority authz.Authority
		skillID   string
		action    authz.Action
		wantErr   error
	}{
		{"owner writes own user skill", userAuth(owner.ID, false), userSkillID, authz.ActionWrite, nil},
		{"owner deletes own user_agent skill", userAuth(owner.ID, false), userAgentSkillID, authz.ActionDelete, nil},
		{"foreign write denied", userAuth(other.ID, false), userSkillID, authz.ActionWrite, ErrNotFound},
		{"non-admin write system denied", userAuth(other.ID, false), systemSkillID, authz.ActionWrite, ErrForbidden},
		{"admin writes system skill", userAuth(adminID, true), systemSkillID, authz.ActionWrite, nil},
		{"delegated executor writes own", agentAuth(owner.ID, "sys"), userAgentSkillID, authz.ActionWrite, nil},
	}
	for _, tc := range writeCases {
		t.Run(tc.name, func(t *testing.T) {
			acc, err := svc.Begin(ctx, tc.authority)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			_, err = acc.AuthorizeManageByID(ctx, tc.skillID, tc.action)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("write = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("write = %v, want %v", err, tc.wantErr)
			}
		})
	}

	// Scope-management: owner may create user/user_agent; non-admin may not manage
	// system scopes; admin may.
	scopeCases := []struct {
		name      string
		authority authz.Authority
		scope     string
		agentID   string
		wantErr   error
	}{
		{"owner manages user scope", userAuth(owner.ID, false), ScopeUser, "", nil},
		{"owner manages user_agent scope", userAuth(owner.ID, false), ScopeUserAgent, "sys", nil},
		{"non-admin manage system denied", userAuth(owner.ID, false), ScopeSystem, "", ErrForbidden},
		{"non-admin manage system_agent denied", userAuth(owner.ID, false), ScopeSystemAgent, "sys", ErrForbidden},
		{"admin manages system scope", userAuth(adminID, true), ScopeSystem, "", nil},
		{"admin manages system_agent scope", userAuth(adminID, true), ScopeSystemAgent, "sys", nil},
		{"delegated executor manages user_agent", agentAuth(owner.ID, "sys"), ScopeUserAgent, "sys", nil},
	}
	for _, tc := range scopeCases {
		t.Run(tc.name, func(t *testing.T) {
			acc, err := svc.Begin(ctx, tc.authority)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			_, _, err = acc.AuthorizeManageScope(ctx, tc.scope, tc.agentID)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("scope = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("scope = %v, want %v", err, tc.wantErr)
			}
		})
	}

	// A group turn (GroupAgentActor, no user) reconstructed from context reads the
	// shared system/system_agent skills it could see pre-cutover, but user-owned
	// skills stay hidden — never a whole-call error.
	t.Run("group turn reads system skills and hides user skills", func(t *testing.T) {
		gctx := authz.WithGroupID(authz.WithAgentID(ctx, "sys"), "group-1")
		dec, err := svc.BeginRead(gctx)
		if err != nil {
			t.Fatalf("group BeginRead: %v", err)
		}
		if ok, err := dec.AllowRead(gctx, systemSkillID, ScopeSystem, "", ""); err != nil || !ok {
			t.Fatalf("group system read = (%v,%v), want (true,nil)", ok, err)
		}
		if ok, err := dec.AllowRead(gctx, systemAgentSkillID, ScopeSystemAgent, "", "sys"); err != nil || !ok {
			t.Fatalf("group system_agent read = (%v,%v), want (true,nil)", ok, err)
		}
		if ok, err := dec.AllowRead(gctx, userSkillID, ScopeUser, owner.ID, ""); err != nil || ok {
			t.Fatalf("group user read = (%v,%v), want (false,nil) — user skills hidden from a group turn", ok, err)
		}
	})
}

type skillIdentityFixture map[string]skills.Skill

func (store skillIdentityFixture) GetIdentity(_ context.Context, id string) (*skills.Skill, error) {
	skill, ok := store[id]
	if !ok {
		return nil, nil
	}
	return &skill, nil
}

func mustCreateSkill(t *testing.T, store skillIdentityFixture, sk skills.Skill) string {
	t.Helper()
	if sk.Status == "" {
		sk.Status = "active"
	}
	sk.ID = uuid.NewString()
	store[sk.ID] = sk
	return sk.ID
}
