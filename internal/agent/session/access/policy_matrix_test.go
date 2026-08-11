package access

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	sessioninbox "github.com/CherryHQ/stella/internal/agent/session/inbox"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	storepkg "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type sessionMatrix struct {
	svc            *Service
	db             sqlc.DBTX
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

func TestResolveInboxDeliveryReauthorizesEndpointsAndRejectsUnavailableTargets(t *testing.T) {
	m := newSessionMatrix(t)
	target, err := m.svc.ResolveInboxDelivery(t.Context(), m.internal, m.private, m.agent)
	if err != nil {
		t.Fatalf("ResolveInboxDelivery: %v", err)
	}
	if target.ID != m.private || target.UserID != m.owner || target.AgentID != m.agent {
		t.Fatalf("target = %+v", target)
	}
	if _, err := m.svc.ResolveInboxDelivery(t.Context(), m.internal, m.groupSID, m.agent); !errors.Is(err, sessioninbox.ErrTargetUnavailable) {
		t.Fatalf("group target error = %v, want ErrTargetUnavailable", err)
	}
	if _, err := m.db.Exec(t.Context(), `UPDATE ctx_conversation SET archived = true WHERE session_id = $1`, m.private); err != nil {
		t.Fatalf("archive target: %v", err)
	}
	if _, err := m.svc.ResolveInboxDelivery(t.Context(), m.internal, m.private, m.agent); !errors.Is(err, sessioninbox.ErrTargetUnavailable) {
		t.Fatalf("archived target error = %v, want ErrTargetUnavailable", err)
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

	t.Run("successful workspace mutation with capability close failure has unknown outcome", func(t *testing.T) {
		closeErr := errors.New("close failed")
		original := m.svc.homes
		m.svc.homes = closeFailingWorkspace{err: closeErr}
		t.Cleanup(func() { m.svc.homes = original })
		access, err := m.svc.Begin(ctx, user(m.owner))
		if err != nil {
			t.Fatal(err)
		}
		_, err = access.CreateWorkspacePath(ctx, WorkspaceCreateInput{
			AgentID: m.agent, SessionID: m.private, Scope: WorkspaceScopeUser,
			Path: "close-outcome.txt", Content: "published",
		})
		if !errors.Is(err, home.ErrOutcomeUnknown) || !errors.Is(err, closeErr) {
			t.Fatalf("CreateWorkspacePath error=%v, want unknown close outcome", err)
		}
		path := filepath.Join(workspaceRootForScope(m.owner, m.agent, WorkspaceScopeUser), "close-outcome.txt")
		if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "published" {
			t.Fatalf("published data=%q err=%v", data, readErr)
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

	t.Run("transcript search preserves user agent and group boundaries", func(t *testing.T) {
		m.svc.searcher = staticSessionSearcher{results: []memory.SearchResult{
			{SourceType: "message", SourceID: uuid.NewString(), SessionID: m.private, Content: "private match"},
			{SourceType: "message", SourceID: uuid.NewString(), SessionID: m.groupSID, Content: "group match"},
		}}
		ownerAccess, err := m.svc.Begin(ctx, worker(m.owner, m.agent))
		if err != nil {
			t.Fatal(err)
		}
		// Invalid synthetic source IDs are dropped after the source Session passes
		// policy; the group row must be dropped before source resolution.
		results, err := ownerAccess.searchRecall(ctx, m.agent, "match", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 0 {
			t.Fatalf("group search hit crossed into private corpus: %#v", results)
		}

		foreignAccess, err := m.svc.Begin(ctx, worker(m.other, m.agent))
		if err != nil {
			t.Fatal(err)
		}
		results, err = foreignAccess.searchRecall(ctx, m.agent, "match", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 0 {
			t.Fatalf("cross-user search exposed sessions: %#v", results)
		}

		groupAccess, err := m.svc.Begin(ctx, group(m.group, m.agent))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := groupAccess.searchRecall(ctx, m.agent, "match", 20); !errors.Is(err, ErrNotFound) {
			t.Fatalf("group transcript search error=%v, want ErrNotFound", err)
		}
		if _, err := ownerAccess.searchRecall(ctx, "other-agent", "match", 20); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-agent transcript search error=%v, want ErrNotFound", err)
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

// TestSessionSendRestrictionMatrix pins the exact sendable policy after actor
// provenance: owned conversation/managed sessions are callable; control-plane,
// archived, group, and cross-principal targets remain unavailable.
func TestSessionSendRestrictionMatrix(t *testing.T) {
	m := newSessionMatrix(t)
	managedID := "managed-send-target"
	seedManagedSession(t, m, managedID)
	archivedID := "archived-send-target"
	now := time.Now().UTC()
	if err := m.svc.memory.SaveInfo(context.Background(), memory.SessionInfo{
		ID: archivedID, UserID: m.owner, AgentID: m.agent, Kind: string(agentsession.KindChat),
		Channel: string(agentsession.ChannelWeb), Archived: true, CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlc.New(m.db).CreateConversation(context.Background(), sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: archivedID, Kind: string(agentsession.KindChat), Channel: string(agentsession.ChannelWeb),
		Archived: true, LastActive: now, UserID: pgtype.Text{String: m.owner, Valid: true}, AgentID: pgtype.Text{String: m.agent, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntimeService{}
	if err := m.svc.BindRuntimeManager(fakeRuntimeManager{svc: runtime}); err != nil {
		t.Fatal(err)
	}
	tool := NewTool(m.svc)
	ownerCtx := memory.WithSessionID(authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent), "source-session")

	cases := []struct {
		name string
		ctx  context.Context
		id   string
		want string
	}{
		{name: "owner sends managed session", ctx: ownerCtx, id: managedID},
		{name: "owner sends channel-bound conversation transcript only", ctx: ownerCtx, id: m.private},
		{name: "control plane sessions are not generic targets", ctx: ownerCtx, id: m.internal, want: "control-plane sessions"},
		{name: "archived conversation is refused", ctx: ownerCtx, id: archivedID, want: "archived"},
		{name: "foreign user is hidden", ctx: memory.WithSessionID(authz.WithAgentID(authz.WithUserID(context.Background(), m.other), m.agent), "source-session"), id: managedID, want: "session not found"},
		{name: "foreign agent is hidden", ctx: memory.WithSessionID(authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), "other-agent"), "source-session"), id: managedID, want: "session not found"},
		{name: "group target is hidden", ctx: ownerCtx, id: m.groupSID, want: "session not found"},
		{name: "self send is rejected", ctx: memory.WithSessionID(authz.WithAgentID(authz.WithUserID(context.Background(), m.owner), m.agent), managedID), id: managedID, want: "cannot send to the current session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tool.Execute(tc.ctx, map[string]any{"action": "send", "session_id": tc.id, "message": "continue"})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("send error=%v, want %q", err, tc.want)
			}
		})
	}
	if len(runtime.managedCalls) != 1 || runtime.managedCalls[0].SessionID != managedID {
		t.Fatalf("managed calls=%#v, want exactly the owned managed send", runtime.managedCalls)
	}
	if len(runtime.chatRequests) != 1 || runtime.chatRequests[0].SessionID != m.private || runtime.chatRequests[0].Channel != agentsession.ChannelTelegram {
		t.Fatalf("conversation calls=%#v, want one transcript-only Telegram target turn", runtime.chatRequests)
	}
	if sessionSendable(agentsession.Info{ID: archivedID, Kind: string(agentsession.KindChat), Archived: true}) {
		t.Fatal("archived conversation card is sendable")
	}
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
	save := func(id, userID, groupID, kind string, channel agentsession.Channel) {
		t.Helper()
		now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
		if err := mem.SaveInfo(ctx, memory.SessionInfo{
			ID: id, UserID: userID, GroupID: groupID, AgentID: agentID,
			Channel: string(channel), Kind: kind, CreatedAt: now, LastActive: now,
		}); err != nil {
			t.Fatalf("SaveInfo(%s): %v", id, err)
		}
		if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
			ID: uuid.NewString(), SessionID: id, Channel: string(channel), Kind: kind, LastActive: now,
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
	save(private, owner, "", string(agentsession.KindChat), agentsession.ChannelTelegram)
	save(internal, owner, "", string(agentsession.KindTask), agentsession.ChannelTask)
	save(groupSession, groupID, groupID, string(agentsession.KindChat), agentsession.ChannelWeb)

	blobStore, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.NewStore(t.TempDir(), blobStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentAccess := agentaccess.NewService(store, appdb.NewAuthStore(pool))
	svc, err := NewService(mem, pool, store, assets, agentAccess, WithHomeWorkspace(testWorkspaceViewer{}))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return sessionMatrix{
		svc: svc, db: pool,
		owner: owner, other: other, group: groupID, agent: agentID,
		private: private, internal: internal, groupSID: groupSession,
		dedicatedAgent: dedicatedAgent, dedicatedChan: dedicatedChan,
	}
}
