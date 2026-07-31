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

func newDBService(t *testing.T, db *pgxpool.Pool) *Service {
	t.Helper()
	svc, err := NewService(Config{Store: NewPostgresStore(db), Users: testUsers{true}, Access: testAccess{true}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestServiceIssueDisclosesOnceAndPersistsVerifierOnly(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, ownerID := seedWebhookChannel(t, db, "agent")
	svc := newDBService(t, db)

	issued, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if issued.Capability == "" {
		t.Fatal("Issue did not disclose a one-time capability")
	}
	if issued.Endpoint.Revision != 1 {
		t.Fatalf("initial revision = %d, want 1", issued.Endpoint.Revision)
	}

	// Positive persistence: provider/owner/token verifier live on the endpoint
	// row, and the stored hash is not the disclosed plaintext.
	var provider, owner, hash, last4 string
	if err := db.QueryRow(ctx,
		"SELECT provider, owner_user_id, token_hash, token_last4 FROM channel_webhook_endpoint WHERE channel_id = $1", channelID,
	).Scan(&provider, &owner, &hash, &last4); err != nil {
		t.Fatalf("scan endpoint row: %v", err)
	}
	if provider != string(ProviderGeneric) || owner != ownerID {
		t.Fatalf("persisted provider/owner = %q/%q, want generic/%q", provider, owner, ownerID)
	}
	if hash == "" || hash == issued.Capability {
		t.Fatalf("token_hash must be a non-plaintext verifier, got %q", hash)
	}
	if last4 != issued.Endpoint.TokenLast4 {
		t.Fatalf("token_last4 = %q, want %q", last4, issued.Endpoint.TokenLast4)
	}

	// Stable read is redacted by construction: the Endpoint type carries no
	// capability field, only display-safe metadata.
	endpoint, err := svc.GetByChannel(ctx, channelID)
	if err != nil {
		t.Fatalf("GetByChannel: %v", err)
	}
	if endpoint.OwnerUserID != ownerID || endpoint.Provider != ProviderGeneric {
		t.Fatalf("stable endpoint = %+v", endpoint)
	}
	if _, err := svc.Resolve(ctx, issued.Capability); err != nil {
		t.Fatalf("Resolve(issued): %v", err)
	}
}

func TestServiceRotateInvalidatesOldTokenUnderCAS(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, ownerID := seedWebhookChannel(t, db, "agent")
	svc := newDBService(t, db)

	issued, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	rotated, err := svc.Rotate(ctx, channelID, issued.Endpoint.ETag())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rotated.Endpoint.Revision != issued.Endpoint.Revision+1 {
		t.Fatalf("rotated revision = %d, want %d", rotated.Endpoint.Revision, issued.Endpoint.Revision+1)
	}
	if _, err := svc.Resolve(ctx, issued.Capability); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token error = %v, want ErrNotFound", err)
	}
	if _, err := svc.Resolve(ctx, rotated.Capability); err != nil {
		t.Fatalf("Resolve(rotated): %v", err)
	}
	// A stale precondition (the pre-rotation etag) is rejected.
	if _, err := svc.Rotate(ctx, channelID, issued.Endpoint.ETag()); !errors.Is(err, ErrStaleETag) {
		t.Fatalf("stale rotate error = %v, want ErrStaleETag", err)
	}
}

func TestServiceRotateRejectsStaleETagAfterRevokeRecreate(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, ownerID := seedWebhookChannel(t, db, "agent")
	svc := newDBService(t, db)

	first, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric})
	if err != nil {
		t.Fatalf("first Issue: %v", err)
	}
	staleETag := first.Endpoint.ETag()

	if _, err := svc.Delete(ctx, channelID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Recreated endpoint restarts at revision 1 but with a fresh token_public_id,
	// so the pre-revoke etag (also revision 1) must not match: no ABA reuse.
	second, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric})
	if err != nil {
		t.Fatalf("second Issue: %v", err)
	}
	if second.Endpoint.Revision != 1 {
		t.Fatalf("recreated revision = %d, want 1", second.Endpoint.Revision)
	}
	if _, err := svc.Rotate(ctx, channelID, staleETag); !errors.Is(err, ErrStaleETag) {
		t.Fatalf("stale-after-recreate rotate error = %v, want ErrStaleETag", err)
	}
	// The fresh etag still works.
	if _, err := svc.Rotate(ctx, channelID, second.Endpoint.ETag()); err != nil {
		t.Fatalf("fresh rotate: %v", err)
	}
}

func TestServiceRotateRejectsCrossChannelETag(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelA, ownerID := seedWebhookChannel(t, db, "agent-a")
	channelB, _ := seedWebhookChannel(t, db, "agent-b")
	svc := newDBService(t, db)

	issuedA, err := svc.Issue(ctx, IssueRequest{ChannelID: channelA, OwnerUserID: ownerID, Provider: ProviderGeneric})
	if err != nil {
		t.Fatalf("Issue A: %v", err)
	}
	if _, err := svc.Issue(ctx, IssueRequest{ChannelID: channelB, OwnerUserID: ownerID, Provider: ProviderGeneric}); err != nil {
		t.Fatalf("Issue B: %v", err)
	}
	// Channel A's etag (same revision 1 as B) must not authorize rotating B.
	if _, err := svc.Rotate(ctx, channelB, issuedA.Endpoint.ETag()); !errors.Is(err, ErrStaleETag) {
		t.Fatalf("cross-channel rotate error = %v, want ErrStaleETag", err)
	}
}

func TestServiceConcurrentSameETagRotateOneWinner(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, ownerID := seedWebhookChannel(t, db, "agent")
	svc := newDBService(t, db)

	issued, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan RotationResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			res, err := svc.Rotate(ctx, channelID, issued.Endpoint.ETag())
			if err != nil {
				errs <- err
				return
			}
			results <- res
		})
	}
	wg.Wait()
	close(results)
	close(errs)

	if len(results) != 1 || len(errs) != 1 {
		t.Fatalf("got %d successes and %d errors, want 1 and 1", len(results), len(errs))
	}
	if err := <-errs; !errors.Is(err, ErrStaleETag) {
		t.Fatalf("loser error = %v, want ErrStaleETag", err)
	}
	winner := <-results
	if _, err := svc.Resolve(ctx, winner.Capability); err != nil {
		t.Fatalf("winning capability does not resolve: %v", err)
	}
}

func TestServiceRevokeInvalidatesToken(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, ownerID := seedWebhookChannel(t, db, "agent")
	svc := newDBService(t, db)

	issued, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	deleted, err := svc.Delete(ctx, channelID)
	if err != nil || !deleted {
		t.Fatalf("Delete = %v, %v; want true, nil", deleted, err)
	}
	if _, err := svc.Resolve(ctx, issued.Capability); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token error = %v, want ErrNotFound", err)
	}
	if again, err := svc.Delete(ctx, channelID); err != nil || again {
		t.Fatalf("second Delete = %v, %v; want false, nil", again, err)
	}
}

func TestServiceIssueRejectsConcurrentEndpoint(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	channelID, ownerID := seedWebhookChannel(t, db, "agent")
	svc := newDBService(t, db)

	if _, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric}); err != nil {
		t.Fatalf("first Issue: %v", err)
	}
	if _, err := svc.Issue(ctx, IssueRequest{ChannelID: channelID, OwnerUserID: ownerID, Provider: ProviderGeneric}); !errors.Is(err, ErrEndpointExists) {
		t.Fatalf("second Issue error = %v, want ErrEndpointExists", err)
	}
}

// seedWebhookChannel inserts an active owner, agent, and webhook channel and
// returns the channel and owner ids.
func seedWebhookChannel(t *testing.T, db *pgxpool.Pool, agentName string) (string, string) {
	t.Helper()
	ctx := context.Background()
	ownerID := uuid.Must(uuid.NewV7()).String()
	channelID := "channel-" + uuid.Must(uuid.NewV7()).String()
	agentID := agentName + "-" + channelID
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", ownerID, ownerID+"@example.test"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO agent (id, name, workspace, enabled) VALUES ($1, $2, $3, true)", agentID, agentName, "/tmp"); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO channel (id, type, agent_id, enabled) VALUES ($1, 'webhook', $2, true)", channelID, agentID); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return channelID, ownerID
}
