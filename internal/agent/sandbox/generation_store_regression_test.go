package sandbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

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

// recreationTestBackend registers a controller-backed backend named
// "recreation-test" whose controller records termination proofs.
func recreationTestBackend(t *testing.T) (*BackendRegistry, *acceptanceController) {
	t.Helper()
	controller := &acceptanceController{}
	registry, err := NewBackendRegistry(BackendDefinition{
		Name: "recreation-test",
		Create: func(context.Context, BackendRequest) (pkgsandbox.Session, error) {
			return nil, errors.New("recreation-test backend is only driven through GenerationSpec.Create")
		},
		ControllerFactory: func(context.Context) (pkgsandbox.ResourceController, error) {
			return controller, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry, controller
}

func TestGenerationSessionRecreatesDeadGenerationThroughDurableProof(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	var created []*acceptanceResource
	registry, controller := recreationTestBackend(t)
	store := NewGenerationStore(db, owner, registry)
	spec := GenerationSpec{
		SessionID:    sessionID,
		Backend:      "recreation-test",
		ConfigDigest: "same-policy",
		Create: func(_ context.Context, generation int64, _ string) (pkgsandbox.Session, error) {
			resource := &acceptanceResource{
				Session:  pkgsandbox.NopSession(),
				identity: pkgsandbox.ResourceIdentity{Backend: "recreation-test", Authority: "authority", Ref: fmt.Sprintf("gen-%d", generation)},
			}
			created = append(created, resource)
			return resource, nil
		},
	}
	managed, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	first := managed.(*generationSession).current

	// A locally dead backend (crash or exited process) must not be silently
	// reused. The next operation recreates through the durable store, which
	// fences the old generation and proves absence through the controller.
	if err := first.raw.Close(); err != nil {
		t.Fatalf("kill raw backend: %v", err)
	}
	if _, err := managed.Exec(t.Context(), "fresh", pkgsandbox.ExecOptions{}); err != nil {
		t.Fatalf("exec after recreation: %v", err)
	}
	second := managed.(*generationSession).current
	if second == nil || second == first {
		t.Fatal("next operation did not bind a recreated generation")
	}
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Generation != 2 || row.State != GenerationActive {
		t.Fatalf("recreated generation = %+v, want generation 2 active", row)
	}
	if got := controller.terminations.Load(); got != 1 {
		t.Fatalf("controller terminations = %d, want exactly one absence proof", got)
	}
	if got := len(created); got != 2 {
		t.Fatalf("backend creates = %d, want 2", got)
	}
}

func TestGenerationSessionEnvRefreshSurvivesPolicyAndDiesWithRecreation(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	var created []*acceptanceResource
	registry, _ := recreationTestBackend(t)
	store := NewGenerationStore(db, owner, registry)
	spec := GenerationSpec{
		SessionID:    sessionID,
		Backend:      "recreation-test",
		ConfigDigest: "same-policy",
		Create: func(_ context.Context, generation int64, _ string) (pkgsandbox.Session, error) {
			resource := &acceptanceResource{
				Session:  pkgsandbox.NopSession(),
				identity: pkgsandbox.ResourceIdentity{Backend: "recreation-test", Authority: "authority", Ref: fmt.Sprintf("gen-%d", generation)},
			}
			created = append(created, resource)
			return resource, nil
		},
	}
	managed, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	refresher, ok := managed.(pkgsandbox.EnvRefresher)
	if !ok {
		t.Fatal("managed session lost the EnvRefresher capability")
	}
	refresher.RefreshEnv(map[string]string{"OAUTH_TOKEN": "rotated-1"})
	if policy := managed.Policy(); policy.Env["OAUTH_TOKEN"] != "rotated-1" {
		t.Fatalf("refreshed credential missing from Policy: %v", policy.Env)
	}

	// Recreation rebuilds the generation from current credential state and
	// must discard the incremental overlay instead of resurrecting it.
	current := managed.(*generationSession).current
	if err := current.raw.Close(); err != nil {
		t.Fatalf("kill raw backend: %v", err)
	}
	if _, err := managed.Exec(t.Context(), "fresh", pkgsandbox.ExecOptions{}); err != nil {
		t.Fatalf("exec after recreation: %v", err)
	}
	if managed.(*generationSession).current == current {
		t.Fatal("generation was not recreated")
	}
	if policy := managed.Policy(); policy.Env["OAUTH_TOKEN"] != "" {
		t.Fatalf("recreated generation resurrected a rotated-away credential: %v", policy.Env)
	}
}

// TestGenerationRefreshDuringRecreationSurvives pins the previous
// ResilientSession ordering: a credential refresh that races a recreation is
// applied after the new generation resets its overlay, so the rotation is not
// lost.
func TestGenerationRefreshDuringRecreationSurvives(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	registry, _, _ := acceptanceProofBackend(t)
	store := NewGenerationStore(db, owner, registry)
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	spec := GenerationSpec{
		SessionID:    sessionID,
		Backend:      "proof-test",
		ConfigDigest: "same-policy",
		Create: func(ctx context.Context, generation int64, _ string) (pkgsandbox.Session, error) {
			if generation == 2 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return &acceptanceResource{
				Session:  pkgsandbox.NopSession(),
				identity: pkgsandbox.ResourceIdentity{Backend: "proof-test", Authority: "control-domain", Ref: fmt.Sprintf("gen-%d", generation)},
			}, nil
		},
	}
	managed, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	current := managed.(*generationSession).current
	if err := current.raw.Close(); err != nil {
		t.Fatalf("kill raw backend: %v", err)
	}
	executed := make(chan error, 1)
	go func() {
		_, err := managed.Exec(t.Context(), "noop", pkgsandbox.ExecOptions{})
		executed <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recreation did not start")
	}
	refreshed := make(chan struct{})
	go func() {
		managed.(pkgsandbox.EnvRefresher).RefreshEnv(map[string]string{"AUDIT_TOKEN": "latest"})
		close(refreshed)
	}()
	// Give an unserialized refresh a chance to return early; a correct
	// implementation keeps it blocked until recreation finishes.
	select {
	case <-refreshed:
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce()
	select {
	case err := <-executed:
		if err != nil {
			t.Fatalf("exec after recreation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recreation did not finish")
	}
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not finish")
	}
	if got := managed.Policy().Env["AUDIT_TOKEN"]; got != "latest" {
		t.Fatalf("credential refresh during recreation was lost: got %q, want latest", got)
	}
}

// TestGenerationReleasedRecreationDoesNotConsumeCapacity pins that a borrow
// installed by an in-flight recreation is released when the runner's Close
// races that recreation; otherwise the destroyed generation holds a retention
// slot forever.
func TestGenerationReleasedRecreationDoesNotConsumeCapacity(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	registry, _, _ := acceptanceProofBackend(t)
	store := NewGenerationStore(db, owner, registry)
	store.maxRetained = 2
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	spec := GenerationSpec{
		SessionID:    sessionID,
		Backend:      "proof-test",
		ConfigDigest: "same-policy",
		Create: func(ctx context.Context, generation int64, _ string) (pkgsandbox.Session, error) {
			if generation == 2 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return &acceptanceResource{
				Session:  pkgsandbox.NopSession(),
				identity: pkgsandbox.ResourceIdentity{Backend: "proof-test", Authority: "control-domain", Ref: uuid.NewString()},
			}, nil
		},
	}
	managed, err := store.Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	current := managed.(*generationSession).current
	if err := current.raw.Close(); err != nil {
		t.Fatalf("kill raw backend: %v", err)
	}
	executed := make(chan error, 1)
	go func() {
		_, err := managed.Exec(t.Context(), "noop", pkgsandbox.ExecOptions{})
		executed <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recreation did not begin")
	}
	closed := make(chan error, 1)
	go func() { closed <- managed.Close() }()
	closeDone := false
	select {
	case err := <-closed:
		closeDone = true
		if err != nil {
			t.Fatalf("runner retirement: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce()
	select {
	case err := <-executed:
		if err != nil {
			t.Fatalf("exec after recreation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recreation did not end")
	}
	if !closeDone {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatalf("runner retirement: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("borrow release did not end")
		}
	}
	if err := store.CloseSessionOwner(t.Context(), sessionID); err != nil {
		t.Fatalf("owner cleanup: %v", err)
	}
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil || row.State != GenerationDestroyed {
		t.Fatalf("original resource not destroyed: %+v %v", row, err)
	}
	for i := range 2 {
		id := uuid.NewString()
		if _, err := db.Exec(t.Context(), `INSERT INTO ctx_conversation(session_id) VALUES ($1)`, id); err != nil {
			t.Fatal(err)
		}
		next := spec
		next.SessionID = id
		borrowed, err := store.Open(t.Context(), next)
		if err != nil {
			t.Fatalf("destroyed, released generation still consumes capacity: new session %d failed: %v", i+1, err)
		}
		t.Cleanup(func() { _ = borrowed.Close() })
	}
}
