package webhook

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestPostgresStoreGitHubSecretCiphertextAndRotation(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, ownerID := seedWebhookChannel(t, db, "agent")
	store := NewPostgresStore(db)
	svc, err := NewService(Config{Store: store, Users: testUsers{true}, Access: testAccess{true}, Cipher: testCipher{}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGitHub, GitHub: GitHubPolicy{Events: []string{"push"}, Repositories: []string{"acme/repo"}}})
	if err != nil {
		t.Fatal(err)
	}
	var hash, ciphertext string
	if err := db.QueryRow(ctx, "SELECT token_hash, provider_secret_ciphertext FROM channel_webhook_endpoint WHERE id = $1", issued.Endpoint.ID).Scan(&hash, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if hash == issued.Capability || ciphertext == issued.GitHubWebhookSecret || ciphertext == "" {
		t.Fatal("plaintext endpoint material persisted")
	}
	rotated, err := svc.Rotate(ctx, issued.Endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveCapability(ctx, issued.Capability); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token error = %v, want ErrNotFound", err)
	}
	if _, err := svc.ResolveCapability(ctx, rotated.Capability); err != nil {
		t.Fatalf("rotated token resolve: %v", err)
	}
}

func TestPostgresStoreResolveRejectsDisabledOwnerChannelAndAgent(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, ownerID := seedWebhookChannel(t, db, "agent")
	store := NewPostgresStore(db)
	svc, err := NewService(Config{Store: store, Users: testUsers{true}, Access: testAccess{true}, Cipher: testCipher{}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name, statement string
		args            []any
	}{
		{"owner", "UPDATE auth_user SET is_active = false WHERE id = $1", []any{ownerID}},
		{"channel", "UPDATE channel SET enabled = false WHERE id = $1", []any{channelID}},
		{"agent", "UPDATE agent SET enabled = false WHERE id = (SELECT agent_id FROM channel WHERE id = $1)", []any{channelID}},
	} {
		t.Run(change.name, func(t *testing.T) {
			if _, err := db.Exec(ctx, change.statement, change.args...); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ResolveCapability(ctx, issued.Capability); !errors.Is(err, ErrNotFound) {
				t.Fatalf("resolve error = %v, want ErrNotFound", err)
			}
			if _, err := db.Exec(ctx, "UPDATE auth_user SET is_active = true WHERE id = $1", ownerID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, "UPDATE channel SET enabled = true WHERE id = $1", channelID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, "UPDATE agent SET enabled = true WHERE id = (SELECT agent_id FROM channel WHERE id = $1)", channelID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresStoreIssueRejectsConcurrentEndpoint(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, ownerID := seedWebhookChannel(t, db, "agent")
	svc, err := NewService(Config{Store: NewPostgresStore(db), Users: testUsers{true}, Access: testAccess{true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric}); !errors.Is(err, ErrEndpointExists) {
		t.Fatalf("second issue error = %v, want ErrEndpointExists", err)
	}
}

func TestPostgresStoreDeliveryClaimRaceExpiryAndGlobalCleanup(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelA, ownerID := seedWebhookChannel(t, db, "agent-a")
	channelB, _ := seedWebhookChannel(t, db, "agent-b")
	store := NewPostgresStore(db)
	endpointA := createEndpoint(t, store, channelA, ownerID)
	endpointB := createEndpoint(t, store, channelB, ownerID)

	const deliveryID = "race-delivery"
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Go(func() {
			won, err := store.ClaimDelivery(ctx, endpointB.ID, ProviderGitHub, deliveryID)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			results <- won
		})
	}
	wg.Wait()
	close(results)
	wins := 0
	for won := range results {
		if won {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("winning claims = %d, want 1", wins)
	}

	if _, err := store.ClaimDelivery(ctx, endpointA.ID, ProviderGitHub, "expired-on-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE channel_webhook_delivery SET created_at = now() - interval '31 days' WHERE endpoint_id = $1", endpointA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimDelivery(ctx, endpointB.ID, ProviderGitHub, "cleanup-on-b"); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM channel_webhook_delivery WHERE endpoint_id = $1", endpointA.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("endpoint B did not globally clean endpoint A's expired row: %d remain", remaining)
	}

	if _, err := store.ClaimDelivery(ctx, endpointB.ID, ProviderGitHub, "expired-redelivery"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE channel_webhook_delivery SET created_at = now() - interval '31 days' WHERE endpoint_id = $1 AND delivery_id = 'expired-redelivery'", endpointB.ID); err != nil {
		t.Fatal(err)
	}
	won, err := store.ClaimDelivery(ctx, endpointB.ID, ProviderGitHub, "expired-redelivery")
	if err != nil || !won {
		t.Fatalf("claim after 30-day window = %v, %v", won, err)
	}
}

func seedWebhookChannel(t *testing.T, db *pgxpool.Pool, agentID string) (string, string) {
	t.Helper()
	ctx := context.Background()
	ownerID := uuid.Must(uuid.NewV7()).String()
	channelID := "channel-" + uuid.Must(uuid.NewV7()).String()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", ownerID, ownerID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO agent (id, name, workspace, enabled) VALUES ($1, $2, $3, true)", agentID+"-"+channelID, agentID, "/tmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO channel (id, type, agent_id, enabled) VALUES ($1, 'webhook', $2, true)", channelID, agentID+"-"+channelID); err != nil {
		t.Fatal(err)
	}
	return channelID, ownerID
}

func createEndpoint(t *testing.T, store *PostgresStore, channelID, ownerID string) Endpoint {
	t.Helper()
	row, err := store.BindEndpoint(context.Background(), channelID, func(_ context.Context, binding ChannelBinding) (endpointRecord, error) {
		if binding.AgentID == "" {
			return endpointRecord{}, ErrNotFound
		}
		return endpointRecord{Endpoint: Endpoint{
			ID: uuid.Must(uuid.NewV7()).String(), OwnerUserID: ownerID, Provider: ProviderGitHub, TokenLast4: "last",
		}, TokenPublicID: uuid.Must(uuid.NewV7()).String(), TokenHash: "hash", ProviderSecretCiphertext: "cipher"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return row.Endpoint
}
