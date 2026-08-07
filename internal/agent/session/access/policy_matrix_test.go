package access

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	storepkg "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type sessionMatrix struct {
	svc            *Service
	owner          string
	other          string
	group          string
	agent          string
	private        string
	internal       string
	groupSID       string
	dedicatedAgent string
	dedicatedChan  string
}

func TestAdminGuestSessionAccessIsLimitedToInspectionAndDeletion(t *testing.T) {
	authority, err := authz.NewUserAuthority("admin", true)
	if err != nil {
		t.Fatal(err)
	}
	access := &Access{authority: authority}
	facts := sessionFactsFor(agentsession.Info{UserID: "guest", GuestID: "guest", AgentID: "agent"}, authority)
	for _, tc := range []struct {
		action authz.Action
		want   bool
	}{
		{action: authz.ActionRead, want: true},
		{action: authz.ActionDelete, want: true},
		{action: authz.ActionWrite},
		{action: authz.ActionExecute},
		{action: authz.ActionCreate},
	} {
		if got := access.allowSession(tc.action, facts); got != tc.want {
			t.Fatalf("allowSession(%s) = %v, want %v", tc.action, got, tc.want)
		}
	}
	if access.allowWorkspace(authz.ActionRead, facts) {
		t.Fatal("admin was allowed to access a guest workspace")
	}
}

// TestEmbeddedPostgresSessionBehaviorMatrix asserts the direct Session/Workspace
// rules over real durable fixtures: who may read/use/create/delete which session,
// how groups/workers/system/dedicated channels are confined, and that collection
// listing and workspace access enforce the same visibility as single reads.
func TestEmbeddedPostgresSessionBehaviorMatrix(t *testing.T) {
	ctx := context.Background()
	m := newSessionMatrix(t)

	user := func(id string) authz.Authority {
		t.Helper()
		authority, err := authz.NewUserAuthority(authz.UserID(id), false)
		if err != nil {
			t.Fatal(err)
		}
		return authority
	}
	worker := func(owner, agent string) authz.Authority {
		t.Helper()
		authority, err := authz.NewAgentAuthority(authz.UserID(owner), authz.AgentID(agent))
		if err != nil {
			t.Fatal(err)
		}
		return authority
	}
	group := func(id, agent string) authz.Authority {
		t.Helper()
		authority, err := authz.NewGroupAgentAuthority(authz.GroupID(id), authz.AgentID(agent))
		if err != nil {
			t.Fatal(err)
		}
		return authority
	}
	dedicated := func(owner, channelID string) authz.Authority {
		t.Helper()
		authority, err := authz.NewChannelAuthority(authz.UserID(owner), false, channelID)
		if err != nil {
			t.Fatal(err)
		}
		return authority
	}
	system := func() authz.Authority {
		t.Helper()
		authority, err := authz.NewSystemAuthority("session-matrix")
		if err != nil {
			t.Fatal(err)
		}
		return authority
	}

	cases := []struct {
		name      string
		authority authz.Authority
		run       func(*Access) error
		wantErr   error
	}{
		{
			name:      "owner reads private session",
			authority: user(m.owner),
			run: func(a *Access) error {
				_, err := a.Read(ctx, m.agent, m.private)
				return err
			},
		},
		{
			name:      "foreign user is hidden",
			authority: user(m.other),
			run: func(a *Access) error {
				_, err := a.Read(ctx, m.agent, m.private)
				return err
			},
			wantErr: ErrNotFound,
		},
		{
			name:      "exact group agent reads its group session during channel resolution",
			authority: group(m.group, m.agent),
			run: func(a *Access) error {
				_, err := a.Read(ctx, m.agent, m.groupSID)
				return err
			},
		},
		{
			name:      "exact group agent uses its group session",
			authority: group(m.group, m.agent),
			run: func(a *Access) error {
				_, err := a.Use(ctx, m.agent, m.groupSID)
				return err
			},
		},
		{
			name:      "other group agent cannot cross group boundary",
			authority: group(uuid.NewString(), m.agent),
			run: func(a *Access) error {
				_, err := a.Use(ctx, m.agent, m.groupSID)
				return err
			},
			wantErr: ErrNotFound,
		},
		{
			name:      "durable agent uses exact owner executor",
			authority: worker(m.owner, m.agent),
			run: func(a *Access) error {
				_, err := a.Use(ctx, m.agent, m.internal)
				return err
			},
		},
		{
			name:      "durable agent authorizes archival of its worker session",
			authority: worker(m.owner, m.agent),
			run: func(a *Access) error {
				_, err := a.Delete(ctx, m.agent, m.internal)
				return err
			},
		},
		{
			name:      "durable agent cannot write its worker session",
			authority: worker(m.owner, m.agent),
			run: func(a *Access) error {
				_, err := a.Write(ctx, m.agent, m.internal)
				return err
			},
			wantErr: ErrNotFound,
		},
		{
			name:      "dedicated channel resolves its exact restricted agent session",
			authority: dedicated(m.owner, m.dedicatedChan),
			run: func(a *Access) error {
				_, err := a.EnsureRead(ctx, agentsession.Request{
					ID: "dedicated-session", UserID: m.owner, AgentID: m.dedicatedAgent,
					Kind: agentsession.KindChat, Channel: agentsession.ChannelTelegram,
					CreateIfMissing: true, AllowExactIDCreate: true,
				})
				return err
			},
		},
		{
			name:      "durable agent cannot switch executor",
			authority: worker(m.owner, "other-agent"),
			run: func(a *Access) error {
				_, err := a.Use(ctx, m.agent, m.internal)
				return err
			},
			wantErr: ErrNotFound,
		},
		{
			name:      "system actor with exact grant uses session",
			authority: system(),
			run: func(a *Access) error {
				_, err := a.Use(ctx, m.agent, m.internal)
				return err
			},
		},
		{
			name:      "system actor gets no workspace access",
			authority: system(),
			run: func(a *Access) error {
				_, err := a.Workspace(ctx, m.agent, m.internal, authz.ActionRead)
				return err
			},
			wantErr: ErrNotFound,
		},
		{
			// Internal sessions are omitted from default lists, but their owner may
			// still inspect or resume one by exact ID. This preserves the existing
			// human recovery contract while every access stays explicit.
			name:      "owner can read internal kind by exact ID",
			authority: user(m.owner),
			run: func(a *Access) error {
				_, err := a.Read(ctx, m.agent, m.internal)
				return err
			},
		},
		{
			name:      "route agent mismatch is hidden",
			authority: user(m.owner),
			run: func(a *Access) error {
				_, err := a.Read(ctx, "other-agent", m.private)
				return err
			},
			wantErr: ErrNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			access, err := m.svc.Begin(ctx, tc.authority)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			err = tc.run(access)
			if tc.wantErr == nil && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}

	t.Run("workspace path traversal is rejected", func(t *testing.T) {
		access, err := m.svc.Begin(ctx, user(m.owner))
		if err != nil {
			t.Fatal(err)
		}
		_, err = access.CreateWorkspacePath(ctx, WorkspaceCreateInput{
			AgentID: m.agent, SessionID: m.private, Scope: WorkspaceScopeUser,
			Path: "../escaped.txt", Content: "unsafe",
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("CreateWorkspacePath error=%v, want ErrInvalid", err)
		}
		root := workspaceRootForScope(m.owner, m.agent, WorkspaceScopeUser)
		if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("path escaped workspace: stat error=%v, want not exist", err)
		}
	})

	t.Run("workspace read cannot follow a symlink outside its root", func(t *testing.T) {
		access, err := m.svc.Begin(ctx, user(m.owner))
		if err != nil {
			t.Fatal(err)
		}
		root := workspaceRootForScope(m.owner, m.agent, WorkspaceScopeUser)
		outside := filepath.Join(t.TempDir(), "secret.txt")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "escape-link")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		_, err = access.ReadWorkspacePath(ctx, WorkspaceReadInput{
			AgentID: m.agent, SessionID: m.private, Scope: WorkspaceScopeUser, Path: "escape-link",
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ReadWorkspacePath error=%v, want ErrNotFound", err)
		}
	})

	t.Run("list filters per-session visibility", func(t *testing.T) {
		access, err := m.svc.Begin(ctx, user(m.owner))
		if err != nil {
			t.Fatal(err)
		}
		infos, err := access.List(ctx, m.agent, agentsession.ListOptions{IncludeArchived: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(infos) != 2 {
			t.Fatalf("list count=%d, want owner private and internal sessions", len(infos))
		}
	})

	t.Run("worker actor lists only its owner executor sessions", func(t *testing.T) {
		access, err := m.svc.Begin(ctx, worker(m.owner, m.agent))
		if err != nil {
			t.Fatal(err)
		}
		infos, err := access.List(ctx, m.agent, agentsession.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(infos) != 2 {
			t.Fatalf("worker list count=%d, want owner private and internal sessions", len(infos))
		}
		if _, err := access.List(ctx, "other-agent", agentsession.ListOptions{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("worker cross-agent List error=%v, want ErrNotFound", err)
		}
		if _, err := access.ListPage(ctx, "other-agent", agentsession.ListOptions{}, 20); !errors.Is(err, ErrNotFound) {
			t.Fatalf("worker cross-agent ListPage error=%v, want ErrNotFound", err)
		}
	})

	t.Run("group actor cannot list sessions", func(t *testing.T) {
		access, err := m.svc.Begin(ctx, group(m.group, m.agent))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := access.List(ctx, m.agent, agentsession.ListOptions{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("group List error=%v, want ErrNotFound", err)
		}
	})

	// Keep creation last because the matrix intentionally shares one durable
	// fixture and list assertions above freeze its pre-create row set.
	t.Run("durable agent creates a worker session for its owner", func(t *testing.T) {
		access, err := m.svc.Begin(ctx, worker(m.owner, m.agent))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := access.Create(ctx, m.owner, m.agent, "", agentsession.KindTask, agentsession.ChannelTask); err != nil {
			t.Fatal(err)
		}
	})

	// A turn running inside a chat holds only the turn's reconstructed authority,
	// never the human's. Rotation is Delete+Create in one evaluation: if either
	// shape stopped being allowed here, `/new` would fail closed in production
	// while the registry's own tests still passed.
	t.Run("durable agent rotates its owner's main session", func(t *testing.T) {
		access, err := m.svc.Begin(ctx, worker(m.owner, m.agent))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := access.RotateMain(ctx, m.owner, m.agent, ""); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("exact group agent rotates its group chat binding", func(t *testing.T) {
		access, err := m.svc.Begin(ctx, group(m.group, m.agent))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := access.RotateChannel(ctx, agentsession.ChannelRequest{
			UserID: m.group, GroupID: m.group, AgentID: m.agent,
			Channel: agentsession.Channel("group:" + m.group),
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func newSessionMatrix(t *testing.T) sessionMatrix {
	t.Helper()
	config.ResetStellaHome()
	t.Setenv("STELLA_HOME", t.TempDir())
	t.Cleanup(config.ResetStellaHome)

	ctx := context.Background()
	pool := dbtest.New(t)
	store := storepkg.NewDBStore(pool)
	owner, other, groupID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	const (
		agentID        = "matrix-system-agent"
		dedicatedAgent = "matrix-dedicated-agent"
		dedicatedChan  = "matrix-dedicated-channel"
	)
	if err := store.CreateAgent(ctx, config.Agent{ID: agentID, Name: "matrix", Model: "test/model", Scope: config.AgentScopeSystem, Enabled: true}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if err := store.CreateAgent(ctx, config.Agent{ID: dedicatedAgent, Name: "dedicated", Model: "test/model", Scope: config.AgentScopeRestricted, Enabled: true}); err != nil {
		t.Fatalf("CreateAgent(dedicated): %v", err)
	}
	if err := store.UpsertChannel(ctx, config.Channel{ID: dedicatedChan, Name: "dedicated", Type: "telegram", AgentID: dedicatedAgent, Enabled: true, Config: `{}`}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	mem := memorytest.New()
	q := sqlc.New(pool)
	save := func(id, userID, groupID, kind string) {
		t.Helper()
		now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
		if err := mem.SaveInfo(ctx, memory.SessionInfo{
			ID: id, UserID: userID, GroupID: groupID, AgentID: agentID,
			Channel: string(agentsession.ChannelWeb), Kind: kind, CreatedAt: now, LastActive: now,
		}); err != nil {
			t.Fatalf("SaveInfo(%s): %v", id, err)
		}
		if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
			ID: uuid.NewString(), SessionID: id, Channel: string(agentsession.ChannelWeb), Kind: kind, LastActive: now,
			AgentID: pgtype.Text{String: agentID, Valid: true}, UserID: pgtype.Text{String: userID, Valid: true},
			GroupID: pgtype.Text{String: groupID, Valid: groupID != ""},
		}); err != nil {
			t.Fatalf("CreateConversation(%s): %v", id, err)
		}
	}
	private, internal, groupSession := "private", "internal", "group"
	if _, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID: groupID, Platform: "test", PlatformGroupID: "group", GroupName: "matrix group",
	}); err != nil {
		t.Fatalf("CreateGroupState: %v", err)
	}
	save(private, owner, "", string(agentsession.KindChat))
	save(internal, owner, "", string(agentsession.KindTask))
	save(groupSession, groupID, groupID, string(agentsession.KindChat))

	blobStore, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.NewStore(t.TempDir(), blobStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentAccess := agentaccess.NewService(store, appdb.NewAuthStore(pool))
	svc, err := NewService(mem, pool, store, assets, agentAccess)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return sessionMatrix{
		svc:   svc,
		owner: owner, other: other, group: groupID, agent: agentID,
		private: private, internal: internal, groupSID: groupSession,
		dedicatedAgent: dedicatedAgent, dedicatedChan: dedicatedChan,
	}
}
