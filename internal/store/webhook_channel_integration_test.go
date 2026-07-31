package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/store"
)

// seedWebhookChannelWithEndpoint inserts an active owner, agent, webhook channel,
// and an active endpoint row, returning the channel id, agent id, and owner id.
func seedWebhookChannelWithEndpoint(t *testing.T, db *pgxpool.Pool) (channelID, agentID, ownerID string) {
	t.Helper()
	ctx := context.Background()
	ownerID = uuid.Must(uuid.NewV7()).String()
	channelID = "channel-" + uuid.Must(uuid.NewV7()).String()
	agentID = "agent-" + channelID
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", ownerID, ownerID+"@example.test"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO agent (id, name, workspace, enabled) VALUES ($1, 'a', '/tmp', true)", agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO channel (id, type, agent_id, enabled, owner_user_id) VALUES ($1, 'webhook', $2, true, $3)", channelID, agentID, ownerID); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO channel_webhook_endpoint (channel_id, provider, token_public_id, token_hash, token_last4)
		 VALUES ($1, 'generic', $2, 'hash', 'aaaa')`,
		channelID, "pub-"+channelID,
	); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	return channelID, agentID, ownerID
}

func TestUpdateChannelBlocksRebindWhileEndpointActive(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	s := store.NewDBStore(db)
	channelID, agentID, _ := seedWebhookChannelWithEndpoint(t, db)

	// Rebinding the Agent while the endpoint is active is rejected.
	if err := s.UpdateChannel(ctx, config.Channel{ID: channelID, Type: "webhook", AgentID: "different-agent", Enabled: true}); !errors.Is(err, config.ErrChannelEndpointActive) {
		t.Fatalf("rebind error = %v, want ErrChannelEndpointActive", err)
	}
	// Changing the type while active is rejected.
	if err := s.UpdateChannel(ctx, config.Channel{ID: channelID, Type: "telegram", AgentID: agentID, Enabled: true}); !errors.Is(err, config.ErrChannelEndpointActive) {
		t.Fatalf("retype error = %v, want ErrChannelEndpointActive", err)
	}
	// Behavior-only writes (name, enabled) remain safe.
	if err := s.UpdateChannel(ctx, config.Channel{ID: channelID, Type: "webhook", AgentID: agentID, Name: "renamed", Enabled: false}); err != nil {
		t.Fatalf("behavior-only update: %v", err)
	}
	got, err := s.GetChannel(ctx, channelID)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if got.Name != "renamed" || got.Enabled {
		t.Fatalf("behavior-only update not applied: %+v", got)
	}
}

func TestDeleteChannelBlockedByActiveEndpoint(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	s := store.NewDBStore(db)
	channelID, _, _ := seedWebhookChannelWithEndpoint(t, db)

	if err := s.DeleteChannel(ctx, channelID); !errors.Is(err, config.ErrChannelEndpointActive) {
		t.Fatalf("delete with active endpoint = %v, want ErrChannelEndpointActive", err)
	}
	if _, err := db.Exec(ctx, "DELETE FROM channel_webhook_endpoint WHERE channel_id = $1", channelID); err != nil {
		t.Fatalf("revoke endpoint: %v", err)
	}
	if err := s.DeleteChannel(ctx, channelID); err != nil {
		t.Fatalf("delete after revoke: %v", err)
	}
}

func TestOwnerDeleteRestrictedWhileEndpointActive(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, _, ownerID := seedWebhookChannelWithEndpoint(t, db)

	_, err := db.Exec(ctx, "DELETE FROM auth_user WHERE id = $1", ownerID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || (pgErr.Code != "23001" && pgErr.Code != "23503") {
		t.Fatalf("owner delete with active endpoint = %v, want FK RESTRICT (23001)", err)
	}
	if _, err := db.Exec(ctx, "DELETE FROM channel_webhook_endpoint WHERE channel_id = $1", channelID); err != nil {
		t.Fatalf("revoke endpoint: %v", err)
	}
	if _, err := db.Exec(ctx, "DELETE FROM channel WHERE id = $1", channelID); err != nil {
		t.Fatalf("delete owned channel: %v", err)
	}
	if _, err := db.Exec(ctx, "DELETE FROM auth_user WHERE id = $1", ownerID); err != nil {
		t.Fatalf("owner delete after revoke: %v", err)
	}
}
