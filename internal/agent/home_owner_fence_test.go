package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type ownerFenceRunner struct{ closed bool }

func (*ownerFenceRunner) Chat(context.Context, []ai.Message, agentruntime.MessageContent) <-chan agentruntime.Event {
	out := make(chan agentruntime.Event)
	close(out)
	return out
}
func (*ownerFenceRunner) Alive() bool             { return true }
func (*ownerFenceRunner) Busy() bool              { return false }
func (*ownerFenceRunner) LastActivity() time.Time { return time.Now() }
func (*ownerFenceRunner) SystemPrompt() string    { return "" }
func (r *ownerFenceRunner) Close() error          { r.closed = true; return nil }

type signalingFenceAcquirer struct {
	delegate home.OwnerFenceAcquirer
	entered  chan struct{}
	once     sync.Once
}

func (f *signalingFenceAcquirer) AcquireHomeOwnerFence(ctx context.Context, kind home.OwnerKind, id string) (home.OwnerFenceLease, error) {
	f.once.Do(func() { close(f.entered) })
	return f.delegate.AcquireHomeOwnerFence(ctx, kind, id)
}

func TestHomeOwnerDeletionWaitsForWorkspaceAdmissionWithoutDeadlock(t *testing.T) {
	for _, kind := range []home.OwnerKind{home.OwnerUser, home.OwnerGroup, home.OwnerAgent} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			db := dbtest.New(t)
			q := sqlc.New(db)
			userID, agentID := uuid.NewString(), uuid.NewString()
			if _, err := db.Exec(ctx, "INSERT INTO auth_user (id,email) VALUES ($1,$2)", userID, userID+"@test.invalid"); err != nil {
				t.Fatal(err)
			}
			if err := q.SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if kind == home.OwnerGroup {
				if _, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: userID, Platform: "test", PlatformGroupID: userID, GroupName: "group"}); err != nil {
					t.Fatal(err)
				}
			}
			local, err := home.NewLocalStore("local", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			registry, err := home.NewRegistry(db, local.ID(), local)
			if err != nil {
				t.Fatal(err)
			}
			factoryEntered := make(chan struct{})
			releaseFactory := make(chan struct{})
			var factoryOnce sync.Once
			rt, err := agentruntime.New(agentruntime.Config{
				Memory: memorytest.New(),
				NewRunner: func(ctx context.Context, _ agentruntime.RunnerParams) (agentruntime.Runner, error) {
					factoryOnce.Do(func() {
						close(factoryEntered)
						<-releaseFactory
					})
					req := home.WorkspaceRequest{UserID: userID, AgentID: agentID}
					if kind == home.OwnerGroup {
						req.GroupID = userID
					}
					if _, err := registry.WorkspaceView(ctx, req); err != nil {
						return nil, err
					}
					return &ownerFenceRunner{}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			pm := NewPoolManager(nil, memorytest.New())
			svc := &Service{Runtime: rt, AgentID: agentID, lifecycle: pm.lifecycle}
			pm.services[agentID] = svc
			fencer := &signalingFenceAcquirer{delegate: pm, entered: make(chan struct{})}
			deletion, err := home.NewOwnerDeletion(db, registry, fencer)
			if err != nil {
				t.Fatal(err)
			}

			admitDone := make(chan error, 1)
			go func() {
				info := session.Info{ID: uuid.NewString(), UserID: userID, AgentID: agentID, Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}
				if kind == home.OwnerGroup {
					info.GroupID = userID
				}
				_, err := svc.admit(ctx, info, "turn")
				admitDone <- err
			}()
			<-factoryEntered // admissionMu is held; WorkspaceView owner gate is not.
			deleteDone := make(chan error, 1)
			go func() {
				switch kind {
				case home.OwnerUser:
					deleteDone <- deletion.DeleteUser(ctx, userID, "operator")
				case home.OwnerGroup:
					deleteDone <- deletion.DeleteGroup(ctx, userID, "operator")
				case home.OwnerAgent:
					deleteDone <- deletion.DeleteAgent(ctx, agentID, "operator")
				}
			}()
			<-fencer.entered
			// With the former Home-gate-first order, deletion owns the Home gate
			// here and releasing the factory creates the exact lock cycle.
			close(releaseFactory)
			select {
			case err := <-admitDone:
				if err != nil {
					t.Fatalf("admission that won ordering failed: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("admission deadlocked")
			}
			select {
			case err := <-deleteDone:
				if err != nil {
					t.Fatalf("delete: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("deletion deadlocked")
			}
			if kind == home.OwnerAgent && pm.GetService(agentID) != nil {
				t.Fatal("Agent service remained published after commit")
			}
			if kind != home.OwnerAgent {
				req := home.WorkspaceRequest{UserID: userID, AgentID: agentID}
				if kind == home.OwnerGroup {
					req.GroupID = userID
				}
				if _, err := registry.WorkspaceView(ctx, req); err == nil {
					t.Fatal("WorkspaceView admitted after user tombstone")
				}
			} else if _, err := q.GetAgent(ctx, agentID); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("Agent owner remains: %v", err)
			}
		})
	}
}

func TestAgentOwnerDeletionRollbackKeepsServicePublished(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	userID, agentID := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id,email) VALUES ($1,$2)", userID, userID+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if err := q.SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO webhook (id,user_id,agent_id,name,provider,wait_timeout_seconds,max_run_timeout_seconds,token_public_id,token_hash,token_last4) VALUES ($1,$2,$3,'x','generic',60,300,'public','hash','1234')", uuid.NewString(), userID, agentID); err != nil {
		t.Fatal(err)
	}
	local, err := home.NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := home.NewRegistry(db, local.ID(), local)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := agentruntime.New(agentruntime.Config{Memory: memorytest.New(), NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
		return &ownerFenceRunner{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	pm := NewPoolManager(nil, memorytest.New())
	svc := &Service{Runtime: rt, AgentID: agentID, lifecycle: pm.lifecycle}
	pm.services[agentID] = svc
	deletion, err := home.NewOwnerDeletion(db, registry, pm)
	if err != nil {
		t.Fatal(err)
	}
	if err := deletion.DeleteAgent(ctx, agentID, "operator"); err == nil {
		t.Fatal("FK-blocked Agent deletion succeeded")
	}
	if pm.GetService(agentID) != svc {
		t.Fatal("failed transaction unpublished Agent service")
	}
	if _, err := q.GetAgent(ctx, agentID); err != nil {
		t.Fatalf("failed transaction deleted Agent owner: %v", err)
	}
	if _, err := svc.admit(ctx, session.Info{ID: uuid.NewString(), UserID: userID, AgentID: agentID, Kind: string(session.KindChat), Channel: string(session.ChannelWeb)}, "after-rollback"); err != nil {
		t.Fatalf("failed transaction left Agent service unusable: %v", err)
	}
}

func TestAcquireHomeOwnerFenceCancellationDoesNotLeakLifecycleGate(t *testing.T) {
	pm := NewPoolManager(nil, memorytest.New())
	if err := pm.lifecycle.lockShared(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := pm.AcquireHomeOwnerFence(ctx, home.OwnerUser, "user"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireHomeOwnerFence = %v, want deadline", err)
	}
	pm.lifecycle.unlockShared()
	lease, err := pm.AcquireHomeOwnerFence(context.Background(), home.OwnerUser, "user")
	if err != nil {
		t.Fatalf("acquire after cancellation: %v", err)
	}
	lease.Release()
}
