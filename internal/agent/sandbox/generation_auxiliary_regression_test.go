package sandbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestGenerationAuxiliaryAcknowledgeIsAtomicWithParent(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	store := NewGenerationStore(db, owner, nil)
	managed, err := store.Open(t.Context(), GenerationSpec{
		SessionID: sessionID, Backend: "none", ConfigDigest: "aux-ack",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
			return pkgsandbox.NopSession(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	auxID := uuid.NewString()
	if _, err := store.q.CreateSandboxGenerationAuxiliary(t.Context(), sqlc.CreateSandboxGenerationAuxiliaryParams{
		AuxID: auxID, SessionID: row.SessionID, Generation: row.Generation,
		OwnerBootID:    owner,
		MaxAuxiliaries: MaxRetainedPreparationsPerGeneration,
		Role:           auxiliaryRolePreparation, Backend: row.Backend, BackingRoot: "/private/prep",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.q.MarkSandboxGenerationUnknown(t.Context(), sqlc.MarkSandboxGenerationUnknownParams{
		SessionID: row.SessionID, Generation: row.Generation, LastError: "provider outcome lost",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE runtime_executor_boot SET status='drained', drained_at=clock_timestamp() WHERE id=$1`, owner); err != nil {
		t.Fatal(err)
	}

	ack, err := store.AcknowledgeAbsent(t.Context(), sessionID, row.Generation, owner, "operator verified provider absence", true)
	if err != nil {
		t.Fatal(err)
	}
	if ack.State != GenerationDestroyed {
		t.Fatalf("acknowledged generation state=%s, want destroyed", ack.State)
	}
	var termination string
	if err := db.QueryRow(t.Context(), `SELECT termination_state FROM agent_sandbox_generation_auxiliary WHERE aux_id=$1`, auxID).Scan(&termination); err != nil {
		t.Fatal(err)
	}
	if termination != auxiliaryTerminationAbsent {
		t.Fatalf("auxiliary termination=%s, want absent", termination)
	}
}

type orderedAuxiliaryController struct {
	probes     atomic.Int32
	terminates atomic.Int32
}

func (c *orderedAuxiliaryController) Probe(context.Context, pkgsandbox.ResourceIdentity) (pkgsandbox.ResourceObservation, error) {
	c.probes.Add(1)
	return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStatePresent}, nil
}

func (c *orderedAuxiliaryController) Terminate(context.Context, pkgsandbox.ResourceIdentity) (pkgsandbox.ResourceObservation, error) {
	c.terminates.Add(1)
	return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateAbsent}, nil
}

func TestGenerationOwnerCloseDrainsDurableAuxiliaryBeforeParent(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	controller := &orderedAuxiliaryController{}
	parent := &acceptanceResource{
		Session:  pkgsandbox.NopSession(),
		identity: pkgsandbox.ResourceIdentity{Backend: "aux-order", Authority: "authority", Ref: "parent"},
	}
	var parentClosed atomic.Bool
	parent.Session = &closeOrderSession{Session: parent.Session, closed: &parentClosed}
	registry, err := NewBackendRegistry(BackendDefinition{
		Name:              "aux-order",
		Create:            func(context.Context, BackendRequest) (pkgsandbox.Session, error) { return parent, nil },
		ControllerFactory: func(context.Context) (pkgsandbox.ResourceController, error) { return controller, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewGenerationStore(db, owner, registry)
	managed, err := store.Open(t.Context(), GenerationSpec{
		SessionID: sessionID, Backend: "aux-order", ConfigDigest: "aux-order",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) { return parent, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	auxID := uuid.NewString()
	if _, err := store.q.CreateSandboxGenerationAuxiliary(t.Context(), sqlc.CreateSandboxGenerationAuxiliaryParams{
		AuxID: auxID, SessionID: row.SessionID, Generation: row.Generation,
		OwnerBootID:    owner,
		MaxAuxiliaries: MaxRetainedPreparationsPerGeneration,
		Role:           auxiliaryRolePreparation, Backend: row.Backend, BackingRoot: "/private/prep",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.q.MarkSandboxGenerationAuxiliaryIdentity(t.Context(), sqlc.MarkSandboxGenerationAuxiliaryIdentityParams{
		AuxID: auxID, SessionID: row.SessionID, Generation: row.Generation,
		ResourceAuthority: "authority", ResourceRef: "auxiliary",
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.CloseSessionOwner(t.Context(), sessionID); err != nil {
		t.Fatal(err)
	}
	if !parentClosed.Load() {
		t.Fatal("parent raw was not closed")
	}
	if controller.terminates.Load() != 2 {
		t.Fatalf("controller terminations=%d, want auxiliary then parent", controller.terminates.Load())
	}
	var parentState, termination string
	if err := db.QueryRow(t.Context(), `SELECT state FROM agent_sandbox_generation WHERE session_id=$1 AND generation=$2`, sessionID, row.Generation).Scan(&parentState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT termination_state FROM agent_sandbox_generation_auxiliary WHERE aux_id=$1`, auxID).Scan(&termination); err != nil {
		t.Fatal(err)
	}
	if parentState != string(GenerationDestroyed) || termination != auxiliaryTerminationAbsent {
		t.Fatalf("parent state=%s auxiliary termination=%s", parentState, termination)
	}
	_ = managed.Close()
}

func TestGenerationAuxiliaryTerminalStatesCannotBeResurrected(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	store := NewGenerationStore(db, owner, nil)
	managed, err := store.Open(t.Context(), GenerationSpec{
		SessionID: sessionID, Backend: "none", ConfigDigest: "aux-cas",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
			return pkgsandbox.NopSession(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	auxID := uuid.NewString()
	if _, err := store.q.CreateSandboxGenerationAuxiliary(t.Context(), sqlc.CreateSandboxGenerationAuxiliaryParams{
		AuxID: auxID, SessionID: row.SessionID, Generation: row.Generation, OwnerBootID: owner,
		MaxAuxiliaries: MaxRetainedPreparationsPerGeneration, Role: auxiliaryRolePreparation,
		Backend: row.Backend, BackingRoot: "/private/prep",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.q.MarkSandboxGenerationAuxiliaryExecution(t.Context(), sqlc.MarkSandboxGenerationAuxiliaryExecutionParams{
		AuxID: auxID, ExecutionState: auxiliaryExecutionUnknown, LastError: "lost",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.q.MarkSandboxGenerationAuxiliaryExecution(t.Context(), sqlc.MarkSandboxGenerationAuxiliaryExecutionParams{
		AuxID: auxID, ExecutionState: auxiliaryExecutionSucceeded,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown auxiliary execution resurrection error=%v, want pgx.ErrNoRows", err)
	}
	if _, err := store.q.MarkSandboxGenerationAuxiliaryTermination(t.Context(), sqlc.MarkSandboxGenerationAuxiliaryTerminationParams{
		AuxID: auxID, TerminationState: auxiliaryTerminationAbsent,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.q.MarkSandboxGenerationAuxiliaryTermination(t.Context(), sqlc.MarkSandboxGenerationAuxiliaryTerminationParams{
		AuxID: auxID, TerminationState: auxiliaryTerminationUnknown,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("absent auxiliary termination resurrection error=%v, want pgx.ErrNoRows", err)
	}
}

func TestGenerationAuxiliaryCreationRequiresLiveParentAndHasCapacityCeiling(t *testing.T) {
	db, sessionID, owner, _ := acceptanceGenerationDB(t)
	store := NewGenerationStore(db, owner, nil)
	managed, err := store.Open(t.Context(), GenerationSpec{
		SessionID: sessionID, Backend: "none", ConfigDigest: "aux-cap",
		Create: func(context.Context, int64, string) (pkgsandbox.Session, error) {
			return pkgsandbox.NopSession(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	row, err := store.Inspect(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxRetainedPreparationsPerGeneration {
		if _, err := store.q.CreateSandboxGenerationAuxiliary(t.Context(), sqlc.CreateSandboxGenerationAuxiliaryParams{
			AuxID: uuid.NewString(), SessionID: row.SessionID, Generation: row.Generation, OwnerBootID: owner,
			MaxAuxiliaries: MaxRetainedPreparationsPerGeneration, Role: auxiliaryRolePreparation,
			Backend: row.Backend, BackingRoot: "/private/prep",
		}); err != nil {
			t.Fatalf("create auxiliary %d: %v", i, err)
		}
	}
	if _, err := store.q.CreateSandboxGenerationAuxiliary(t.Context(), sqlc.CreateSandboxGenerationAuxiliaryParams{
		AuxID: uuid.NewString(), SessionID: row.SessionID, Generation: row.Generation, OwnerBootID: owner,
		MaxAuxiliaries: MaxRetainedPreparationsPerGeneration, Role: auxiliaryRolePreparation,
		Backend: row.Backend, BackingRoot: "/private/prep",
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("auxiliary capacity error=%v, want pgx.ErrNoRows", err)
	}
	if _, err := store.q.MarkSandboxGenerationFenced(t.Context(), sqlc.MarkSandboxGenerationFencedParams{
		SessionID: row.SessionID, Generation: row.Generation, LastError: "fenced",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.q.CreateSandboxGenerationAuxiliary(t.Context(), sqlc.CreateSandboxGenerationAuxiliaryParams{
		AuxID: uuid.NewString(), SessionID: row.SessionID, Generation: row.Generation, OwnerBootID: owner,
		MaxAuxiliaries: MaxRetainedPreparationsPerGeneration, Role: auxiliaryRolePreparation,
		Backend: row.Backend, BackingRoot: "/private/prep",
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fenced parent auxiliary create error=%v, want pgx.ErrNoRows", err)
	}
}

type closeOrderSession struct {
	pkgsandbox.Session
	closed *atomic.Bool
}

func (s *closeOrderSession) Close() error {
	s.closed.Store(true)
	return s.Session.Close()
}
