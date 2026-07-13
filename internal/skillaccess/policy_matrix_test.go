package skillaccess

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/skills"
	storepkg "github.com/CherryHQ/stella/internal/store"
)

// countingAuthorizer proves the PEP owns exactly one Begin per use case.
type countingAuthorizer struct {
	authz.Authorizer
	begins int
}

func (a *countingAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	a.begins++
	return a.Authorizer.Begin(ctx, authority)
}

func TestEmbeddedPostgresSkillPolicyMatrix(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	store := storepkg.NewDBStore(pool)
	oidc := appdb.NewOIDCStore(pool)
	assign := appdb.NewAuthStore(pool)
	skillStore := skills.New(pool)

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

	az := &countingAuthorizer{Authorizer: policy.New(pool)}
	svc := NewService(skillStore, agentaccess.NewService(store, assign, az), az)

	userAuth := func(id string, admin bool) authz.Authority {
		role := authz.RoleUser
		if admin {
			role = authz.RoleAdmin
		}
		rs, err := authz.NewRoleSet(role)
		if err != nil {
			t.Fatal(err)
		}
		a, err := authz.NewUserAuthority(authz.UserID(id), rs, authz.GrantSet{})
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

	// One Begin per read use case, whichever authority.
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
		{"admin reads any user skill", userAuth("admin-x", true), userSkillID, nil},
		{"non-admin cannot read system skill by id", userAuth(other.ID, false), systemSkillID, ErrForbidden},
		{"admin reads system skill by id", userAuth("admin-x", true), systemSkillID, nil},
		{"delegated executor reads own user_agent skill", agentAuth(owner.ID, "sys"), userAgentSkillID, nil},
		{"foreign delegated denied", agentAuth(other.ID, "sys"), userAgentSkillID, ErrNotFound},
	}
	for _, tc := range readCases {
		t.Run(tc.name, func(t *testing.T) {
			before := az.begins
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
			if az.begins != before+1 {
				t.Fatalf("Begin count = %d, want 1", az.begins-before)
			}
		})
	}

	// AuthorizeRead is the tool/HTTP resolve/list read path: unlike the by-id
	// management path it does not escalate system reads to admin, so any user or
	// delegated agent may read shared system skills while owner rules still gate
	// user scopes. One Begin per read use case.
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
			before := az.begins
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
			if az.begins != before+1 {
				t.Fatalf("Begin count = %d, want 1", az.begins-before)
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
		{"admin writes system skill", userAuth("admin-x", true), systemSkillID, authz.ActionWrite, nil},
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
		{"admin manages system scope", userAuth("admin-x", true), ScopeSystem, "", nil},
		{"admin manages system_agent scope", userAuth("admin-x", true), ScopeSystemAgent, "sys", nil},
		{"delegated executor manages user_agent", agentAuth(owner.ID, "sys"), ScopeUserAgent, "sys", nil},
	}
	for _, tc := range scopeCases {
		t.Run(tc.name, func(t *testing.T) {
			before := az.begins
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
			if az.begins != before+1 {
				t.Fatalf("Begin count = %d, want 1", az.begins-before)
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
		if ok, err := dec.AllowRead(gctx, systemSkillID, ScopeSystem, "", "", nil); err != nil || !ok {
			t.Fatalf("group system read = (%v,%v), want (true,nil)", ok, err)
		}
		if ok, err := dec.AllowRead(gctx, systemAgentSkillID, ScopeSystemAgent, "", "sys", nil); err != nil || !ok {
			t.Fatalf("group system_agent read = (%v,%v), want (true,nil)", ok, err)
		}
		if ok, err := dec.AllowRead(gctx, userSkillID, ScopeUser, owner.ID, "", nil); err != nil || ok {
			t.Fatalf("group user read = (%v,%v), want (false,nil) — user skills hidden from a group turn", ok, err)
		}
	})

	// The read port carries metadata, so a source-based custom deny applies to a
	// tool/HTTP read exactly as it would with the row loaded server-side. The same
	// skill is readable without the tainted source and hidden with it.
	t.Run("source custom deny applies through the read port", func(t *testing.T) {
		ps := policy.NewService(policy.New(pool))
		if _, _, err := ps.CreatePolicy(ctx, policy.PolicyInput{
			Name: "deny tainted-source skill read", Resource: authz.ResourceSkill, Action: authz.ActionRead,
			Effect: policy.EffectDeny, Subjects: policy.NewSubjectBuilder().Kinds(authz.ActorAgent).Build(),
			Predicates: []policy.Predicate{policy.Eq("source", "tainted")},
		}); err != nil {
			t.Fatal(err)
		}
		actx := authz.WithAgentID(authz.WithUserID(ctx, owner.ID), "sys")
		dec, err := svc.BeginRead(actx)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := dec.AllowRead(actx, userSkillID, ScopeUser, owner.ID, "", []byte(`{}`)); err != nil || !ok {
			t.Fatalf("clean-source read = (%v,%v), want (true,nil)", ok, err)
		}
		if ok, err := dec.AllowRead(actx, userSkillID, ScopeUser, owner.ID, "", []byte(`{"source":"tainted"}`)); err != nil || ok {
			t.Fatalf("tainted-source read = (%v,%v), want (false,nil) — source policy must apply via the port", ok, err)
		}
	})

	// An active custom deny overrides the owner built-in against the durable facts.
	t.Run("custom deny hides own skill", func(t *testing.T) {
		ps := policy.NewService(policy.New(pool))
		if _, _, err := ps.CreatePolicy(ctx, policy.PolicyInput{
			Name: "deny own skill read", Resource: authz.ResourceSkill, Action: authz.ActionRead,
			Effect: policy.EffectDeny, Subjects: policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
			Predicates: []policy.Predicate{policy.Eq("is_owner", "true")},
		}); err != nil {
			t.Fatal(err)
		}
		acc, err := svc.Begin(ctx, userAuth(owner.ID, false))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := acc.AuthorizeManageByID(ctx, userSkillID, authz.ActionRead); !errors.Is(err, ErrNotFound) {
			t.Fatalf("custom deny read = %v, want ErrNotFound", err)
		}
	})
}

func mustCreateSkill(t *testing.T, store *skills.PGStore, sk skills.Skill) string {
	t.Helper()
	if sk.Status == "" {
		sk.Status = "active"
	}
	id, err := store.Create(context.Background(), sk, map[string]string{skills.MainFile: "---\nname: " + sk.Name + "\n---\n"})
	if err != nil {
		t.Fatalf("create skill %q: %v", sk.Name, err)
	}
	return id
}
