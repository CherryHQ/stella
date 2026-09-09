package sandbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestGenerationOpenReconcilesUnknownAliveProvider(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	registry, controller, raw := acceptanceProofBackend(t)
	store := NewGenerationStore(db, owner, registry)
	spec := GenerationSpec{
		SessionID:    sessionID,
		Backend:      "proof-test",
		ConfigDigest: "same-policy",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
			return raw, nil
		},
	}
	first, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	firstGeneration := first.(*generationSession).current.key.generation
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	store.markUnknown(t.Context(), row, "injected uncertain outcome")

	second, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatalf("unknown but alive provider did not reconcile: %v", err)
	}
	if got := controller.terminations.Load(); got != 1 {
		t.Fatalf("termination calls = %d, want 1", got)
	}
	if got := second.(*generationSession).current.key.generation; got != firstGeneration+1 {
		t.Fatalf("generation = %d, want %d", got, firstGeneration+1)
	}
	_ = second.Close()
}

func TestGenerationOwnerCloseProvesAbsenceBeforeRawCleanup(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	resource := &closeOrderResource{Session: pkgsandbox.NopSession(), identity: pkgsandbox.ResourceIdentity{
		Backend: "close-order-test", Authority: "authority", Ref: "resource",
	}}
	controller := &closeOrderController{resource: resource}
	registry, err := NewBackendRegistry(BackendDefinition{
		Name: "close-order-test",
		Create: func(context.Context, BackendRequest) (pkgsandbox.Session, error) {
			return resource, nil
		},
		ControllerFactory: func(context.Context) (pkgsandbox.ResourceController, error) {
			return controller, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewGenerationStore(db, owner, registry)
	managed, err := store.Open(t.Context(), GenerationSpec{
		SessionID: sessionID, Backend: "close-order-test", ConfigDigest: "same-policy",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
			return resource, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := managed.(*generationSession).CloseOwner(); err != nil {
		t.Fatalf("owner close: %v", err)
	}
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != GenerationDestroyed {
		t.Fatalf("owner close state = %s, want destroyed", row.State)
	}
	if controller.probedAfterCleanup.Load() != 0 {
		t.Fatal("controller probed after raw cleanup")
	}
}

func TestGenerationOwnerCloseUsesControllerlessLocalAbsenceProof(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	base := &acceptanceResource{Session: pkgsandbox.NopSession(), identity: pkgsandbox.ResourceIdentity{Backend: "local"}}
	raw := &observedAcceptanceResource{acceptanceResource: base}
	store := NewGenerationStore(db, owner, nil)
	if _, err := store.Open(t.Context(), GenerationSpec{
		SessionID: sessionID, Backend: "local", ConfigDigest: "same-policy",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) { return raw, nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CloseSessionOwner(t.Context(), sessionID); err != nil {
		t.Fatalf("controllerless owner close: %v", err)
	}
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != GenerationDestroyed {
		t.Fatalf("controllerless owner close state = %s, want destroyed", row.State)
	}
	if got := base.closes.Load(); got != 1 {
		t.Fatalf("raw close calls = %d, want 1", got)
	}
}

func TestGenerationCapacityReservationCoversConcurrentCreates(t *testing.T) {
	store := &GenerationStore{
		maxRetained: 1,
		entries:     make(map[generationKey]*generationEntry),
	}
	release, err := store.reserveCapacity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.reserveCapacity(t.Context()); !errors.Is(err, ErrGenerationCapacity) {
		t.Fatalf("second in-flight creation error = %v, want capacity exhaustion", err)
	}
	release()
	releaseAgain, err := store.reserveCapacity(t.Context())
	if err != nil {
		t.Fatalf("reservation after rollback: %v", err)
	}
	releaseAgain()
}

type closeOrderResource struct {
	pkgsandbox.Session
	identity pkgsandbox.ResourceIdentity
	closed   atomic.Bool
}

type observedAcceptanceResource struct{ *acceptanceResource }

func (r *observedAcceptanceResource) ObserveResource(context.Context) (pkgsandbox.ResourceObservation, error) {
	if r.Alive() {
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStatePresent}, nil
	}
	return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateAbsent}, nil
}

func (r *closeOrderResource) ResourceIdentity(context.Context) (pkgsandbox.ResourceIdentity, error) {
	return r.identity, nil
}

func (r *closeOrderResource) Close() error {
	r.closed.Store(true)
	return r.Session.Close()
}

type closeOrderController struct {
	resource           *closeOrderResource
	probedAfterCleanup atomic.Int32
}

func (c *closeOrderController) Probe(context.Context, pkgsandbox.ResourceIdentity) (pkgsandbox.ResourceObservation, error) {
	if c.resource.closed.Load() {
		c.probedAfterCleanup.Add(1)
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: "raw cleanup happened first"}, nil
	}
	return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStatePresent}, nil
}

func (c *closeOrderController) Terminate(context.Context, pkgsandbox.ResourceIdentity) (pkgsandbox.ResourceObservation, error) {
	return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateAbsent}, nil
}

func TestGenerationUncertainStateIsSharedAndNewGenerationIsClean(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	registry, _, _ := acceptanceProofBackend(t)
	store := NewGenerationStore(db, owner, registry)
	var created []*acceptanceResource
	spec := GenerationSpec{
		SessionID:    sessionID,
		Backend:      "proof-test",
		ConfigDigest: "same-policy",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
			resource := &acceptanceResource{
				Session:  pkgsandbox.NopSession(),
				identity: pkgsandbox.ResourceIdentity{Backend: "proof-test", Authority: "control-domain", Ref: "resource"},
			}
			created = append(created, resource)
			return resource, nil
		},
	}
	first, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	oldRaw := first.(*generationSession).current
	if second.(*generationSession).current != oldRaw {
		t.Fatal("same-generation reopen did not share the retained raw session")
	}
	oldRaw.operationError("injected uncertain outcome", errors.New("transport lost"))
	if _, err := first.Exec(t.Context(), "true", pkgsandbox.ExecOptions{}); !errors.Is(err, ErrGenerationUnknown) {
		t.Fatalf("first borrow error = %v, want generation unknown", err)
	}
	if _, err := second.Exec(t.Context(), "true", pkgsandbox.ExecOptions{}); !errors.Is(err, ErrGenerationUnknown) {
		t.Fatalf("second borrow error = %v, want generation unknown", err)
	}

	next, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatalf("next Open did not recover after reconciliation: %v", err)
	}
	nextRaw := next.(*generationSession).current
	if nextRaw == oldRaw {
		t.Fatal("next generation reused the uncertain raw session")
	}
	if err := nextRaw.uncertain(); err != nil {
		t.Fatalf("new generation inherited uncertain state: %v", err)
	}
	if got := len(created); got != 2 {
		t.Fatalf("create calls = %d, want 2", got)
	}
	_ = first.Close()
	_ = second.Close()
	_ = next.Close()
}
