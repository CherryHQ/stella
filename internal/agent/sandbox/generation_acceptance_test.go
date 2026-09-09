package sandbox

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type acceptanceResource struct {
	pkgsandbox.Session
	identity pkgsandbox.ResourceIdentity
	closes   atomic.Int32
	once     sync.Once
}

func (s *acceptanceResource) ResourceIdentity(context.Context) (pkgsandbox.ResourceIdentity, error) {
	return s.identity, nil
}

func (s *acceptanceResource) Close() error {
	s.once.Do(func() {
		s.closes.Add(1)
		_ = s.Session.Close()
	})
	return nil
}

type acceptanceController struct{ terminations atomic.Int32 }

func (c *acceptanceController) Probe(context.Context, pkgsandbox.ResourceIdentity) (pkgsandbox.ResourceObservation, error) {
	return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStatePresent}, nil
}

func (c *acceptanceController) Terminate(context.Context, pkgsandbox.ResourceIdentity) (pkgsandbox.ResourceObservation, error) {
	c.terminations.Add(1)
	return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateAbsent}, nil
}

func acceptanceGenerationDB(t *testing.T) (*pgxpool.Pool, string, string, string) {
	t.Helper()
	db := dbtest.New(t)
	sessionID, ownerA, ownerB := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(t.Context(), `INSERT INTO ctx_conversation(session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{ownerA, ownerB} {
		if _, err := db.Exec(t.Context(), `INSERT INTO runtime_executor_boot(id,status) VALUES ($1,'running')`, owner); err != nil {
			t.Fatal(err)
		}
	}
	return db, sessionID, ownerA, ownerB
}

func TestGenerationAcceptanceIdleCloseReusesHealthyNativeCompute(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	store := NewGenerationStore(db, owner, nil)
	var creates atomic.Int32
	raw := &acceptanceResource{Session: pkgsandbox.NopSession(), identity: pkgsandbox.ResourceIdentity{Backend: "none"}}
	t.Cleanup(func() { _ = raw.Close() })
	spec := GenerationSpec{SessionID: sessionID, Backend: "none", ConfigDigest: "same-policy", Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
		creates.Add(1)
		return raw, nil
	}}
	first, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("ordinary runner retirement: %v", err)
	}
	if got := raw.closes.Load(); got != 0 {
		t.Fatalf("runner retirement closed unprovable native compute %d times", got)
	}
	second, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatalf("new runner could not reuse healthy compute: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Generation != 1 || row.State != GenerationActive || creates.Load() != 1 {
		t.Fatalf("idle reopen changed healthy generation: row=%+v creates=%d", row, creates.Load())
	}
}

func acceptanceProofBackend(t *testing.T) (*BackendRegistry, *acceptanceController, *acceptanceResource) {
	t.Helper()
	controller := &acceptanceController{}
	raw := &acceptanceResource{Session: pkgsandbox.NopSession(), identity: pkgsandbox.ResourceIdentity{Backend: "proof-test", Authority: "control-domain", Ref: strings.Repeat("a", 64)}}
	t.Cleanup(func() { _ = raw.Close() })
	registry, err := NewBackendRegistry(BackendDefinition{
		Name: "proof-test",
		Create: func(context.Context, BackendRequest) (pkgsandbox.Session, error) {
			return raw, nil
		},
		ControllerFactory: func(context.Context) (pkgsandbox.ResourceController, error) { return controller, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry, controller, raw
}

func TestGenerationAcceptanceOtherLiveOwnerCannotTerminateCompute(t *testing.T) {
	db, sessionID, ownerA, ownerB := acceptanceGenerationDB(t)
	registry, controller, raw := acceptanceProofBackend(t)
	storeA, storeB := NewGenerationStore(db, ownerA, registry), NewGenerationStore(db, ownerB, registry)
	spec := GenerationSpec{SessionID: sessionID, Backend: "proof-test", ConfigDigest: "same-policy", Create: func(context.Context, int64, string) (pkgsandbox.Session, error) { return raw, nil }}
	if _, err := storeA.Open(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	_, err := storeB.Open(t.Context(), spec)
	if calls := controller.terminations.Load(); calls != 0 {
		t.Fatalf("second live executor terminated healthy owner's compute %d times (open error %v)", calls, err)
	}
	if err == nil {
		t.Fatal("second executor replaced compute still owned by a healthy boot")
	}
	row, err := storeA.Inspect(t.Context(), sessionID)
	if err != nil || row.Generation != 1 || row.OwnerBootID != ownerA || row.State != GenerationActive {
		t.Fatalf("live owner's generation changed: %+v, %v", row, err)
	}
}

func TestGenerationAcceptanceFailedDurableFenceCannotTerminateResource(t *testing.T) {
	db, sessionID, ownerA, ownerB := acceptanceGenerationDB(t)
	registry, controller, raw := acceptanceProofBackend(t)
	storeA, storeB := NewGenerationStore(db, ownerA, registry), NewGenerationStore(db, ownerB, registry)
	spec := GenerationSpec{SessionID: sessionID, Backend: "proof-test", ConfigDigest: "same-policy", Create: func(context.Context, int64, string) (pkgsandbox.Session, error) { return raw, nil }}
	if _, err := storeA.Open(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE runtime_executor_boot SET status='drained',drained_at=clock_timestamp() WHERE id=$1`, ownerA); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `CREATE FUNCTION reject_generation_fence() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN
            IF NEW.state IN ('fenced','unknown') THEN RAISE EXCEPTION 'injected fence write failure'; END IF;
            RETURN NEW;
        END $$;
        CREATE TRIGGER reject_generation_fence BEFORE UPDATE ON agent_sandbox_generation
        FOR EACH ROW EXECUTE FUNCTION reject_generation_fence();`); err != nil {
		t.Fatal(err)
	}
	_, err := storeB.Open(t.Context(), spec)
	if calls := controller.terminations.Load(); calls != 0 {
		t.Fatalf("resource terminated %d times without a committed fence (open error %v)", calls, err)
	}
	if err == nil {
		t.Fatal("replacement succeeded despite a failed durable fence")
	}
}
