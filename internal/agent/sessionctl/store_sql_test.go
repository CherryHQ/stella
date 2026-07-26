package sessionctl

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func seedConversation(t *testing.T, pool *pgxpool.Pool, sessionID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO ctx_conversation (session_id, agent_id, user_id) VALUES ($1, $2, $3)`,
		sessionID, testAgentID, testUserID)
	if err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
}

func newSQLStore(t *testing.T, sessionID string) NonceStore {
	t.Helper()
	store, _ := newSQLStoreWithPool(t, sessionID)
	return store
}

// newSQLStoreWithPool also hands back the pool, for the assertions that are
// about what the table holds rather than what the store returns.
func newSQLStoreWithPool(t *testing.T, sessionID string) (NonceStore, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.New(t)
	seedConversation(t, pool, sessionID)
	return NewSQLNonceStoreForPool(pool), pool
}

func assertNonceRowCount(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM agent_session_rotation_nonce").Scan(&got); err != nil {
		t.Fatalf("count nonces: %v", err)
	}
	if got != want {
		t.Fatalf("nonce rows = %d, want %d", got, want)
	}
}

func pendingNonce(sessionID string) Nonce {
	return Nonce{
		ID:         uuid.Must(uuid.NewV7()).String(),
		SessionID:  sessionID,
		BindingKey: "main:" + testUserID + ":" + testAgentID,
		ActorID:    "",
		TurnMarker: "turn:1",
		ExpiresAt:  time.Now().UTC().Add(DefaultTTL),
	}
}

func TestSQLNonceStoreRoundTrip(t *testing.T) {
	const sessionID = "sess-roundtrip"
	store := newSQLStore(t, sessionID)
	ctx := context.Background()

	want := pendingNonce(sessionID)
	if err := store.Create(ctx, want); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != want.SessionID || got.BindingKey != want.BindingKey || got.TurnMarker != want.TurnMarker {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if got.Used() {
		t.Fatal("a fresh nonce must not be marked used")
	}
	if !got.ExpiresAt.Equal(got.ExpiresAt.UTC()) {
		t.Fatal("expiry must come back in UTC")
	}
}

func TestSQLNonceStoreUnknownIDIsNotFound(t *testing.T) {
	store := newSQLStore(t, "sess-unknown")
	ctx := context.Background()

	if _, err := store.Get(ctx, uuid.Must(uuid.NewV7()).String()); !errors.Is(err, ErrNonceNotFound) {
		t.Fatalf("unknown id: err = %v, want ErrNonceNotFound", err)
	}
	// A model can put anything in the nonce argument; a non-uuid must read as
	// "no such pending rotation", not as a database type error.
	if _, err := store.Get(ctx, "not-a-uuid"); !errors.Is(err, ErrNonceNotFound) {
		t.Fatalf("malformed id: err = %v, want ErrNonceNotFound", err)
	}
	if _, err := store.Claim(ctx, "not-a-uuid"); !errors.Is(err, ErrNonceNotFound) {
		t.Fatalf("malformed claim: err = %v, want ErrNonceNotFound", err)
	}
}

// TestSQLNonceStoreClaimIsSingleUse is the property the whole two-phase design
// rests on: concurrent confirmations, including ones on different nodes, must
// produce exactly one rotation.
func TestSQLNonceStoreClaimIsSingleUse(t *testing.T) {
	const sessionID = "sess-claim"
	store, pool := newSQLStoreWithPool(t, sessionID)
	ctx := context.Background()

	n := pendingNonce(sessionID)
	if err := store.Create(ctx, n); err != nil {
		t.Fatalf("create: %v", err)
	}

	const racers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     int
		firstErr error
	)
	wg.Add(racers)
	for range racers {
		go func() {
			defer wg.Done()
			_, err := store.Claim(ctx, n.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case !errors.Is(err, ErrNonceNotFound) && firstErr == nil:
				firstErr = err
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("unexpected claim error: %v", firstErr)
	}
	if wins != 1 {
		t.Fatalf("%d concurrent claims succeeded, want exactly 1", wins)
	}

	// Claiming deletes: a spent nonce authorizes nothing, so it leaves no row for
	// a later confirmation to find and none for the sweep to carry.
	if _, err := store.Get(ctx, n.ID); !errors.Is(err, ErrNonceNotFound) {
		t.Fatalf("get after claim: err = %v, want ErrNonceNotFound", err)
	}
	if _, err := store.Claim(ctx, n.ID); !errors.Is(err, ErrNonceNotFound) {
		t.Fatalf("re-claim: err = %v, want ErrNonceNotFound", err)
	}
	assertNonceRowCount(t, pool, 0)
}

// TestSQLNonceStoreCreateSweepsExpiredAcrossBindings pins the table's bound. The
// sweep has to be global: a chat that asks for a reset once and never comes back
// would otherwise keep its expired row forever, because nothing in its own
// binding ever writes again.
func TestSQLNonceStoreCreateSweepsExpiredAcrossBindings(t *testing.T) {
	const sessionID = "sess-sweep"
	store, pool := newSQLStoreWithPool(t, sessionID)
	ctx := context.Background()

	abandoned := pendingNonce(sessionID)
	abandoned.BindingKey = "channel:someone-else:agent:tg"
	abandoned.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if err := store.Create(ctx, abandoned); err != nil {
		t.Fatalf("create abandoned: %v", err)
	}

	fresh := pendingNonce(sessionID)
	if err := store.Create(ctx, fresh); err != nil {
		t.Fatalf("create fresh: %v", err)
	}

	if _, err := store.Get(ctx, abandoned.ID); !errors.Is(err, ErrNonceNotFound) {
		t.Fatalf("expired nonce from another binding survived: err = %v", err)
	}
	if _, err := store.Get(ctx, fresh.ID); err != nil {
		t.Fatalf("the sweep took the live nonce with it: %v", err)
	}
	assertNonceRowCount(t, pool, 1)
}

func TestSQLNonceStoreClaimRejectsExpired(t *testing.T) {
	const sessionID = "sess-expired"
	store := newSQLStore(t, sessionID)
	ctx := context.Background()

	n := pendingNonce(sessionID)
	n.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if err := store.Create(ctx, n); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Get still returns it so the caller can validate every condition itself...
	if _, err := store.Get(ctx, n.ID); err != nil {
		t.Fatalf("get expired: %v", err)
	}
	// ...but it can never be spent.
	if _, err := store.Claim(ctx, n.ID); !errors.Is(err, ErrNonceNotFound) {
		t.Fatalf("claim expired: err = %v, want ErrNonceNotFound", err)
	}
}

// TestSQLNonceStoreCreatePrunesDeadRows keeps the table from growing one row per
// reset request forever, without a background sweeper.
func TestSQLNonceStoreCreatePrunesDeadRows(t *testing.T) {
	const sessionID = "sess-prune"
	store := newSQLStore(t, sessionID)
	ctx := context.Background()

	dead := pendingNonce(sessionID)
	dead.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	if err := store.Create(ctx, dead); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	live := pendingNonce(sessionID)
	if err := store.Create(ctx, live); err != nil {
		t.Fatalf("create live: %v", err)
	}

	if _, err := store.Get(ctx, dead.ID); !errors.Is(err, ErrNonceNotFound) {
		t.Fatalf("expired row should have been pruned, err = %v", err)
	}
	if _, err := store.Get(ctx, live.ID); err != nil {
		t.Fatalf("live row must survive the prune: %v", err)
	}
}
