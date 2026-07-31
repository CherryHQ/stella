package controlplane_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/internal/webhook"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type fakeUsers struct{}

func (fakeUsers) IsActive(context.Context, string) (bool, error) { return true, nil }

type fakeAccess struct{}

func (fakeAccess) CanUseOwner(context.Context, string, string) (bool, error) { return true, nil }

func seedWebhookChannel(t *testing.T, db *pgxpool.Pool) (channelID, ownerID string) {
	t.Helper()
	ctx := context.Background()
	ownerID = uuid.Must(uuid.NewV7()).String()
	channelID = "channel-" + uuid.Must(uuid.NewV7()).String()
	agentID := "agent-" + channelID
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", ownerID, ownerID+"@example.test"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO agent (id, name, workspace, enabled) VALUES ($1, 'a', '/tmp', true)", agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO channel (id, type, agent_id, enabled) VALUES ($1, 'webhook', $2, true)", channelID, agentID); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return channelID, ownerID
}

// TestWebhookEndpointLifecycleAndConflicts drives the admin control-plane
// endpoint lifecycle end to end and asserts the conflict mappings. It wires a
// real store and webhook service with nil runtime dependencies because none of
// these paths touch the plugin host: a delete against an active endpoint is
// rejected at the pre-check before any runtime side effect.
func TestWebhookEndpointLifecycleAndConflicts(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, ownerID := seedWebhookChannel(t, db)

	whSvc, err := webhook.NewService(webhook.Config{
		Store:  webhook.NewPostgresStore(db),
		Users:  fakeUsers{},
		Access: fakeAccess{},
	})
	if err != nil {
		t.Fatalf("webhook.NewService: %v", err)
	}
	svc := controlplane.NewService(store.NewDBStore(db), nil, nil, nil, nil, controlplane.WithWebhookEndpoints(whSvc))

	admin, err := authz.NewUserAuthority(authz.UserID("admin"), true)
	if err != nil {
		t.Fatalf("NewUserAuthority: %v", err)
	}
	access, err := svc.Begin(ctx, admin)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// Get before create is a 404-mapped NotFoundError.
	if _, err := access.GetWebhookEndpoint(ctx, channelID); !isNotFound(err) {
		t.Fatalf("get before create = %v, want NotFoundError", err)
	}

	// Create discloses a one-time url capability and starts at revision 1.
	created, err := access.CreateWebhookEndpoint(ctx, channelID, ownerID, webhook.ProviderGeneric)
	if err != nil {
		t.Fatalf("CreateWebhookEndpoint: %v", err)
	}
	if created.Capability == "" || created.Endpoint.Revision != 1 {
		t.Fatalf("create result = %+v", created.Endpoint)
	}

	// Stable Get returns matching redacted metadata (the domain type has no
	// capability field) with a stable etag.
	got, err := access.GetWebhookEndpoint(ctx, channelID)
	if err != nil {
		t.Fatalf("GetWebhookEndpoint: %v", err)
	}
	if got.ETag() != created.Endpoint.ETag() || got.OwnerUserID != ownerID {
		t.Fatalf("stable get = %+v", got)
	}

	// A second create against an active endpoint is a 409 conflict.
	if _, err := access.CreateWebhookEndpoint(ctx, channelID, ownerID, webhook.ProviderGeneric); !isConflict(err) {
		t.Fatalf("duplicate create = %v, want ConflictError", err)
	}

	// A stale-etag rotate is a 409 conflict; the correct etag succeeds.
	if _, err := access.RotateWebhookEndpoint(ctx, channelID, "whk1-not-a-real-etag"); !isConflict(err) {
		t.Fatalf("stale rotate = %v, want ConflictError", err)
	}
	rotated, err := access.RotateWebhookEndpoint(ctx, channelID, created.Endpoint.ETag())
	if err != nil {
		t.Fatalf("RotateWebhookEndpoint: %v", err)
	}
	if rotated.Endpoint.Revision != created.Endpoint.Revision+1 || rotated.Capability == created.Capability {
		t.Fatalf("rotate result = %+v", rotated.Endpoint)
	}

	// Deleting the channel while the endpoint is active is a 409 conflict,
	// short-circuited before any runtime side effect.
	if err := access.DeleteChannel(ctx, channelID); !isConflict(err) {
		t.Fatalf("delete channel with active endpoint = %v, want ConflictError", err)
	}

	// Revoke, then the endpoint is gone.
	if err := access.DeleteWebhookEndpoint(ctx, channelID); err != nil {
		t.Fatalf("DeleteWebhookEndpoint: %v", err)
	}
	if _, err := access.GetWebhookEndpoint(ctx, channelID); !isNotFound(err) {
		t.Fatalf("get after revoke = %v, want NotFoundError", err)
	}
	if err := access.DeleteWebhookEndpoint(ctx, channelID); !isNotFound(err) {
		t.Fatalf("double revoke = %v, want NotFoundError", err)
	}
}

func isNotFound(err error) bool {
	var nf *controlplane.NotFoundError
	return errors.As(err, &nf)
}

func isConflict(err error) bool {
	var ce *controlplane.ConflictError
	return errors.As(err, &ce)
}
