package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/internal/webhook"
)

type activeWebhookOwner struct{ active bool }

func (o activeWebhookOwner) IsActive(context.Context, string) (bool, error) { return o.active, nil }

type webhookOwnerAccess struct{ allowed bool }

func (a webhookOwnerAccess) CanUseOwner(context.Context, string, string) (bool, error) {
	return a.allowed, nil
}

type blockingWebhookOwnerAccess struct {
	expectedAgent string
	entered       chan struct{}
	release       chan struct{}
	once          sync.Once
}

func (a *blockingWebhookOwnerAccess) CanUseOwner(_ context.Context, _, agentID string) (bool, error) {
	a.once.Do(func() { close(a.entered) })
	<-a.release
	return agentID == a.expectedAgent, nil
}

func seedChannelForUpdate(t *testing.T, db *pgxpool.Pool, agentPrefix string) (channelID, ownerID, agentID string) {
	t.Helper()
	ctx := context.Background()
	ownerID = uuid.Must(uuid.NewV7()).String()
	channelID = "channel-" + uuid.Must(uuid.NewV7()).String()
	agentID = agentPrefix + "-" + channelID
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", ownerID, ownerID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO agent (id, name, workspace, enabled) VALUES ($1, $2, $3, true)", agentID, agentPrefix, "/tmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO channel (id, type, agent_id, enabled, config) VALUES ($1, 'webhook', $2, true, '{\"provider\":\"generic\"}')", channelID, agentID); err != nil {
		t.Fatal(err)
	}
	return channelID, ownerID, agentID
}

func issueGenericEndpoint(t *testing.T, db *pgxpool.Pool, channelID, ownerID string) {
	t.Helper()
	svc, err := webhook.NewService(webhook.Config{
		Store:  webhook.NewPostgresStore(db),
		Users:  activeWebhookOwner{active: true},
		Access: webhookOwnerAccess{allowed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Issue(context.Background(), webhook.IssueRequest{
		ChannelID: channelID, OwnerUserID: ownerID, Provider: webhook.ProviderGeneric,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateChannelPreservesBindingConflict(t *testing.T) {
	db := dbtest.New(t)
	s := store.NewDBStore(db)
	ctx := context.Background()
	channelA, _, agentA := seedChannelForUpdate(t, db, "agent-a")
	channelB, _, _ := seedChannelForUpdate(t, db, "agent-b")
	if _, err := db.Exec(ctx, "UPDATE channel SET type = 'telegram' WHERE id IN ($1, $2)", channelA, channelB); err != nil {
		t.Fatal(err)
	}

	err := s.UpdateChannel(ctx, config.ChannelUpdate{Channel: config.Channel{
		ID: channelB, Type: "telegram", AgentID: agentA, Enabled: true, Config: `{}`,
	}})
	var conflict *config.ChannelBindingConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("update error = %v, want ChannelBindingConflictError", err)
	}
	if conflict.ChannelID != channelA {
		t.Fatalf("conflict channel = %q, want %q", conflict.ChannelID, channelA)
	}
}

func TestUpdateChannelGuardsActiveEndpointBindingAndProvider(t *testing.T) {
	db := dbtest.New(t)
	s := store.NewDBStore(db)
	ctx := context.Background()
	channelID, ownerID, agentID := seedChannelForUpdate(t, db, "agent-a")
	newAgentID := "agent-b-" + channelID
	if _, err := db.Exec(ctx, "INSERT INTO agent (id, name, workspace, enabled) VALUES ($1, $2, $3, true)", newAgentID, "agent-b", "/tmp"); err != nil {
		t.Fatal(err)
	}
	issueGenericEndpoint(t, db, channelID, ownerID)

	behavior := config.ChannelUpdate{
		Channel:          config.Channel{ID: channelID, Name: "renamed", Type: "webhook", AgentID: agentID, Enabled: false, Config: `{"provider":"generic","default_wait":false}`},
		EndpointProvider: "generic",
	}
	if err := s.UpdateChannel(ctx, behavior); err != nil {
		t.Fatalf("behavior update: %v", err)
	}
	got, err := s.GetChannel(ctx, channelID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != behavior.Channel.Name || got.Enabled != behavior.Channel.Enabled || got.Config != behavior.Channel.Config {
		t.Fatalf("behavior update = %+v, want %+v", got, behavior.Channel)
	}

	for _, update := range []config.ChannelUpdate{
		{Channel: behavior.Channel, EndpointProvider: "github"},
		{Channel: config.Channel{ID: channelID, Type: "webhook", AgentID: newAgentID, Enabled: true, Config: `{"provider":"generic"}`}, EndpointProvider: "generic"},
		{Channel: config.Channel{ID: channelID, Type: "telegram", AgentID: agentID, Enabled: true, Config: `{}`}},
	} {
		if err := s.UpdateChannel(ctx, update); !errors.Is(err, config.ErrChannelEndpointActive) {
			t.Fatalf("guarded update error = %v, want ErrChannelEndpointActive", err)
		}
	}
}

func TestIssueAndChannelRebindShareOneLock(t *testing.T) {
	db := dbtest.New(t)
	channels := store.NewDBStore(db)
	ctx := context.Background()
	channelID, ownerID, oldAgentID := seedChannelForUpdate(t, db, "agent-a")
	newAgentID := "agent-b-" + channelID
	if _, err := db.Exec(ctx, "INSERT INTO agent (id, name, workspace, enabled) VALUES ($1, $2, $3, true)", newAgentID, "agent-b", "/tmp"); err != nil {
		t.Fatal(err)
	}

	access := &blockingWebhookOwnerAccess{expectedAgent: oldAgentID, entered: make(chan struct{}), release: make(chan struct{})}
	endpoints, err := webhook.NewService(webhook.Config{Store: webhook.NewPostgresStore(db), Users: activeWebhookOwner{active: true}, Access: access})
	if err != nil {
		t.Fatal(err)
	}
	issued := make(chan error, 1)
	go func() {
		_, err := endpoints.Issue(ctx, webhook.IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: webhook.ProviderGeneric})
		issued <- err
	}()
	select {
	case <-access.entered:
	case <-time.After(time.Second):
		t.Fatal("issuance did not reach owner validation")
	}

	rebound := make(chan error, 1)
	go func() {
		rebound <- channels.UpdateChannel(ctx, config.ChannelUpdate{
			Channel:          config.Channel{ID: channelID, Type: "webhook", AgentID: newAgentID, Enabled: true, Config: `{"provider":"generic"}`},
			EndpointProvider: "generic",
		})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		var waiting int
		if err := db.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%FOR UPDATE OF channel%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			select {
			case err := <-rebound:
				t.Fatalf("rebind bypassed issuance lock: %v", err)
			default:
				t.Fatal("rebind did not block on the channel row")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(access.release)
	select {
	case err := <-issued:
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("issuance did not complete")
	}
	select {
	case err := <-rebound:
		if !errors.Is(err, config.ErrChannelEndpointActive) {
			t.Fatalf("rebind error = %v, want ErrChannelEndpointActive", err)
		}
	case <-time.After(time.Second):
		t.Fatal("rebind stayed blocked after issuance")
	}
	var agentID string
	if err := db.QueryRow(ctx, "SELECT agent_id FROM channel WHERE id = $1", channelID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if agentID != oldAgentID {
		t.Fatalf("channel agent = %q, want %q", agentID, oldAgentID)
	}
}
