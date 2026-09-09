package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	// MaxRetainedPreparationsPerGeneration is a deliberate durable retention
	// ceiling. Proved-absent audit rows remain queryable but do not consume it.
	MaxRetainedPreparationsPerGeneration int64 = 64

	auxiliaryRolePreparation = "prep"

	auxiliaryExecutionCreating  = "creating"
	auxiliaryExecutionSucceeded = "succeeded"
	auxiliaryExecutionFailed    = "failed"
	auxiliaryExecutionUnknown   = "unknown"

	auxiliaryTerminationOpen    = "open"
	auxiliaryTerminationAbsent  = "absent"
	auxiliaryTerminationUnknown = "unknown"
)

// PreparationSpec describes one short-lived auxiliary Session that belongs to
// a durable main generation. BackingRoot is a private compute/cleanup
// coordinate, never a workspace path exposed to the agent.
type PreparationSpec struct {
	SessionID   string
	Generation  int64
	Backend     string
	BackingRoot string
	Create      func(context.Context, int64, string) (pkgsandbox.Session, error)
}

// AuxiliaryRecord is the durable lifecycle of one preparation resource. Its
// execution result and provider termination proof are intentionally separate:
// a failed install can still be absent, while a successful install may remain
// owned when local cleanup cannot prove absence.
type AuxiliaryRecord struct {
	AuxID            string                      `json:"aux_id"`
	SessionID        string                      `json:"session_id"`
	Generation       int64                       `json:"generation"`
	Role             string                      `json:"role"`
	Backend          string                      `json:"backend"`
	BackingRoot      string                      `json:"backing_root"`
	Resource         pkgsandbox.ResourceIdentity `json:"resource"`
	ExecutionState   string                      `json:"execution_state"`
	TerminationState string                      `json:"termination_state"`
	LastError        string                      `json:"last_error"`
	CreatedAt        time.Time                   `json:"created_at"`
	UpdatedAt        time.Time                   `json:"updated_at"`
}

func auxiliaryFromRow(row sqlc.AgentSandboxGenerationAuxiliary) AuxiliaryRecord {
	return AuxiliaryRecord{
		AuxID: row.AuxID, SessionID: row.SessionID, Generation: row.Generation,
		Role: row.Role, Backend: row.Backend, BackingRoot: row.BackingRoot,
		Resource: pkgsandbox.ResourceIdentity{
			Backend: row.Backend, Authority: row.ResourceAuthority, Ref: row.ResourceRef,
		},
		ExecutionState: row.ExecutionState, TerminationState: row.TerminationState,
		LastError: row.LastError, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
}

func validAuxiliaryExecution(value string) bool {
	switch value {
	case auxiliaryExecutionCreating, auxiliaryExecutionSucceeded, auxiliaryExecutionFailed, auxiliaryExecutionUnknown:
		return true
	default:
		return false
	}
}

func validAuxiliaryTermination(value string) bool {
	switch value {
	case auxiliaryTerminationOpen, auxiliaryTerminationAbsent, auxiliaryTerminationUnknown:
		return true
	default:
		return false
	}
}

// Prepare creates and owns one auxiliary preparation Session, runs operation
// through a parent-generation guard, and records both execution and
// termination outcomes. A termination observation of Unknown is returned to
// the caller without failing an otherwise known operation; the private root
// remains owned and must not be removed until a later absence proof.
func (s *GenerationStore) Prepare(ctx context.Context, spec PreparationSpec, operation func(pkgsandbox.Session) error) (pkgsandbox.ResourceObservation, error) {
	if s == nil || s.q == nil {
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: "sandbox generation store is unavailable"}, errors.New("sandbox: generation store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if spec.SessionID == "" || spec.Backend == "" || spec.BackingRoot == "" || spec.Create == nil || operation == nil {
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: "incomplete auxiliary preparation specification"}, errors.New("sandbox: incomplete auxiliary preparation specification")
	}
	parent, err := s.preparationParent(ctx, spec)
	if err != nil {
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: err.Error()}, err
	}
	auxID := uuid.NewString()
	row, err := s.createAuxiliaryLocked(ctx, parent, auxID, spec)
	if err != nil {
		if errors.Is(err, ErrGenerationCapacity) {
			return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: "auxiliary preparation retention capacity exhausted"}, err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if latest, parentErr := s.preparationParent(ctx, spec); parentErr != nil {
				return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: parentErr.Error()}, parentErr
			} else if rows, rowsErr := s.auxiliaryRows(ctx, generationKey{sessionID: latest.SessionID, generation: latest.Generation}); rowsErr == nil {
				open := 0
				for _, aux := range rows {
					if aux.TerminationState != auxiliaryTerminationAbsent {
						open++
					}
				}
				if int64(open) >= MaxRetainedPreparationsPerGeneration {
					return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: "auxiliary preparation retention capacity exhausted"}, ErrGenerationCapacity
				}
			}
		}
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: err.Error()}, fmt.Errorf("create sandbox preparation ledger: %w", err)
	}

	aux := &preparationRawSession{
		store: s, parent: generationKey{sessionID: parent.SessionID, generation: parent.Generation},
		row:      auxiliaryFromRow(row),
		identity: pkgsandbox.ResourceIdentity{Backend: spec.Backend},
	}
	s.auxMu.Lock()
	s.auxEntries[auxID] = aux
	s.auxMu.Unlock()
	// Serialize the complete create/identity/operation/termination sequence
	// against owner shutdown. The store lock is not held while provider I/O
	// runs, so Close can wait here without an ABBA cycle.
	aux.lifecycleMu.Lock()
	defer aux.lifecycleMu.Unlock()

	raw, createErr := spec.Create(ctx, parent.Generation, s.ownerBootID)
	aux.setCreationResult(raw, createErr)
	if createErr != nil || raw == nil {
		detail := "preparation backend creation outcome is unknown"
		if createErr != nil {
			detail = fmt.Sprintf("preparation backend creation outcome is unknown: %v", createErr)
		}
		_, _ = s.markAuxiliaryExecution(ctx, auxID, auxiliaryExecutionUnknown, detail)
		s.markPreparationParentUnknown(ctx, parent, detail)
		if createErr == nil {
			createErr = errors.New("preparation backend returned a nil sandbox session")
		}
		s.forgetAuxiliaryEntry(auxID)
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: detail}, fmt.Errorf("sandbox preparation %s: %w", auxID, createErr)
	}

	if provider, ok := raw.(pkgsandbox.ResourceIdentityProvider); ok {
		identity, identityErr := provider.ResourceIdentity(ctx)
		if identityErr != nil {
			detail := fmt.Sprintf("read preparation resource identity: %v", identityErr)
			aux.markUncertain(identityErr)
			aux.operationError(detail, identityErr)
			return s.finishPreparationLocked(ctx, aux, identityErr)
		}
		if identity.Backend == "" {
			identity.Backend = spec.Backend
		}
		aux.setIdentity(identity)
	}
	rowMeta, identity := aux.metadata()
	identityRow, err := s.q.MarkSandboxGenerationAuxiliaryIdentity(ctx, sqlc.MarkSandboxGenerationAuxiliaryIdentityParams{
		ResourceAuthority: identity.Authority, ResourceRef: identity.Ref,
		AuxID: rowMeta.AuxID, SessionID: rowMeta.SessionID, Generation: rowMeta.Generation,
	})
	if err != nil {
		detail := fmt.Sprintf("persist preparation resource identity: %v", err)
		aux.operationError(detail, err)
		return s.finishPreparationLocked(ctx, aux, err)
	}
	aux.setRecord(auxiliaryFromRow(identityRow))

	operationErr := operation(aux)
	return s.finishPreparationLocked(ctx, aux, operationErr)
}

func (s *GenerationStore) createAuxiliaryLocked(ctx context.Context, parent GenerationRecord, auxID string, spec PreparationSpec) (sqlc.AgentSandboxGenerationAuxiliary, error) {
	type beginner interface {
		Begin(context.Context) (pgx.Tx, error)
	}
	db, ok := s.db.(beginner)
	if !ok {
		return sqlc.AgentSandboxGenerationAuxiliary{}, errors.New("sandbox auxiliary preparation requires a transactional database")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return sqlc.AgentSandboxGenerationAuxiliary{}, err
	}
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockSandboxGenerationAuxiliaryParent(ctx, sqlc.LockSandboxGenerationAuxiliaryParentParams{
		SessionID: parent.SessionID, Generation: parent.Generation, OwnerBootID: s.ownerBootID,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return sqlc.AgentSandboxGenerationAuxiliary{}, err
	}
	count, err := qtx.CountSandboxGenerationOpenAuxiliaries(ctx, sqlc.CountSandboxGenerationOpenAuxiliariesParams{
		SessionID: parent.SessionID, Generation: parent.Generation,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return sqlc.AgentSandboxGenerationAuxiliary{}, err
	}
	if count >= MaxRetainedPreparationsPerGeneration {
		_ = tx.Rollback(ctx)
		return sqlc.AgentSandboxGenerationAuxiliary{}, ErrGenerationCapacity
	}
	row, err := qtx.CreateSandboxGenerationAuxiliary(ctx, sqlc.CreateSandboxGenerationAuxiliaryParams{
		AuxID: auxID, SessionID: parent.SessionID, Generation: parent.Generation,
		OwnerBootID: s.ownerBootID, MaxAuxiliaries: MaxRetainedPreparationsPerGeneration,
		Role: auxiliaryRolePreparation, Backend: spec.Backend, BackingRoot: spec.BackingRoot,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return sqlc.AgentSandboxGenerationAuxiliary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.AgentSandboxGenerationAuxiliary{}, err
	}
	return row, nil
}

func (s *GenerationStore) preparationParent(ctx context.Context, spec PreparationSpec) (GenerationRecord, error) {
	var row sqlc.AgentSandboxGeneration
	var err error
	if spec.Generation == 0 {
		row, err = s.q.GetCurrentSandboxGeneration(ctx, spec.SessionID)
	} else {
		row, err = s.q.GetSandboxGeneration(ctx, sqlc.GetSandboxGenerationParams{SessionID: spec.SessionID, Generation: spec.Generation})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return GenerationRecord{}, ErrGenerationNotFound
	}
	if err != nil {
		return GenerationRecord{}, fmt.Errorf("inspect preparation parent generation: %w", err)
	}
	record := recordFromRow(row)
	if record.OwnerBootID != s.ownerBootID {
		return GenerationRecord{}, fmt.Errorf("%w: preparation parent belongs to executor %s", ErrGenerationBusy, record.OwnerBootID)
	}
	if spec.Generation == 0 && record.State != GenerationActive {
		return GenerationRecord{}, fmt.Errorf("%w: current generation state=%s", ErrGenerationFenced, record.State)
	}
	if record.State != GenerationCreating && record.State != GenerationActive {
		return GenerationRecord{}, fmt.Errorf("%w: preparation parent state=%s", ErrGenerationFenced, record.State)
	}
	if record.Backend != spec.Backend {
		return GenerationRecord{}, fmt.Errorf("sandbox preparation backend %q does not match generation backend %q", spec.Backend, record.Backend)
	}
	return record, nil
}

func (s *GenerationStore) markAuxiliaryExecution(ctx context.Context, auxID, state, detail string) (AuxiliaryRecord, error) {
	if !validAuxiliaryExecution(state) {
		return AuxiliaryRecord{}, fmt.Errorf("sandbox: invalid auxiliary execution state %q", state)
	}
	row, err := s.q.MarkSandboxGenerationAuxiliaryExecution(ctx, sqlc.MarkSandboxGenerationAuxiliaryExecutionParams{AuxID: auxID, ExecutionState: state, LastError: detail})
	if err != nil {
		return AuxiliaryRecord{}, err
	}
	record := auxiliaryFromRow(row)
	s.updateAuxiliaryRecord(auxID, record)
	return record, nil
}

func (s *GenerationStore) markAuxiliaryTermination(ctx context.Context, auxID, state, detail string) (AuxiliaryRecord, error) {
	if !validAuxiliaryTermination(state) {
		return AuxiliaryRecord{}, fmt.Errorf("sandbox: invalid auxiliary termination state %q", state)
	}
	row, err := s.q.MarkSandboxGenerationAuxiliaryTermination(ctx, sqlc.MarkSandboxGenerationAuxiliaryTerminationParams{AuxID: auxID, TerminationState: state, LastError: detail})
	if err != nil {
		return AuxiliaryRecord{}, err
	}
	record := auxiliaryFromRow(row)
	s.updateAuxiliaryRecord(auxID, record)
	s.auxMu.Lock()
	if state == auxiliaryTerminationAbsent {
		delete(s.auxEntries, auxID)
	}
	s.auxMu.Unlock()
	return record, nil
}

func (s *GenerationStore) updateAuxiliaryRecord(auxID string, record AuxiliaryRecord) {
	s.auxMu.Lock()
	aux := s.auxEntries[auxID]
	s.auxMu.Unlock()
	if aux != nil {
		aux.setRecord(record)
	}
}

func (s *GenerationStore) markPreparationParentUnknown(ctx context.Context, parent GenerationRecord, detail string) {
	// Fence the in-process capability before the database write. If the write
	// is unavailable, an active raw session must still fail closed rather than
	// continue while its auxiliary outcome is unknown.
	cause := errors.New(detail)
	key := generationKey{sessionID: parent.SessionID, generation: parent.Generation}
	s.mu.Lock()
	entry := s.entries[key]
	var raw *generationRawSession
	if entry != nil {
		raw = entry.raw
	}
	s.mu.Unlock()
	if raw != nil {
		raw.markUncertain(cause)
	}
	if current, err := s.q.GetSandboxGeneration(ctx, sqlc.GetSandboxGenerationParams{SessionID: parent.SessionID, Generation: parent.Generation}); err == nil {
		s.markUnknown(ctx, recordFromRow(current), detail)
	}
}

func (s *GenerationStore) finishPreparationLocked(ctx context.Context, aux *preparationRawSession, operationErr error) (pkgsandbox.ResourceObservation, error) {
	row, _ := aux.metadata()
	uncertain := aux.uncertain()
	var executionErr error
	switch {
	case uncertain != nil:
		_, executionErr = s.markAuxiliaryExecution(ctx, row.AuxID, auxiliaryExecutionUnknown, fmt.Sprintf("auxiliary execution outcome is unknown: %v", uncertain))
		s.markPreparationParentUnknown(ctx, aux.parentRecord(ctx), fmt.Sprintf("auxiliary preparation outcome is unknown: %v", uncertain))
	case operationErr != nil:
		_, executionErr = s.markAuxiliaryExecution(ctx, row.AuxID, auxiliaryExecutionFailed, operationErr.Error())
	default:
		_, executionErr = s.markAuxiliaryExecution(ctx, row.AuxID, auxiliaryExecutionSucceeded, "")
	}
	observation, _ := s.terminatePreparationLocked(ctx, aux)
	if executionErr != nil {
		if operationErr != nil {
			operationErr = errors.Join(operationErr, executionErr)
		} else {
			operationErr = executionErr
		}
	}
	if uncertain != nil {
		return observation, errors.Join(operationErr, fmt.Errorf("%w: auxiliary execution outcome is unknown: %w", ErrGenerationUnknown, uncertain))
	}
	if operationErr != nil {
		return observation, operationErr
	}
	// A known operation may finish while provider termination remains unknown.
	// Keep the private root and return the observation so callers can retain it
	// for later cleanup without treating install as failed.
	return observation, nil
}

func (s *GenerationStore) terminatePreparation(ctx context.Context, aux *preparationRawSession) (pkgsandbox.ResourceObservation, error) {
	aux.lifecycleMu.Lock()
	defer aux.lifecycleMu.Unlock()
	return s.terminatePreparationLocked(ctx, aux)
}

func (s *GenerationStore) terminatePreparationLocked(ctx context.Context, aux *preparationRawSession) (pkgsandbox.ResourceObservation, error) {
	row, identity := aux.metadata()
	raw := aux.rawSession()
	if raw == nil {
		detail := "preparation backend creation is still unresolved"
		if err := aux.creationError(); err != nil {
			detail = fmt.Sprintf("preparation backend creation outcome is unknown: %v", err)
		}
		_, _ = s.markAuxiliaryTermination(ctx, row.AuxID, auxiliaryTerminationUnknown, detail)
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: detail}, ErrGenerationUnknown
	}
	if identity.Backend == "" {
		identity.Backend = row.Backend
	}
	controller, controllerErr := s.controller(ctx, row.Backend)
	if controllerErr != nil {
		detail := fmt.Sprintf("construct preparation resource controller: %v", controllerErr)
		_, _ = s.markAuxiliaryTermination(ctx, row.AuxID, auxiliaryTerminationUnknown, detail)
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: detail}, controllerErr
	}
	if controller != nil {
		if identity.Authority == "" || identity.Ref == "" {
			detail := "preparation resource identity is unavailable"
			_, _ = s.markAuxiliaryTermination(ctx, row.AuxID, auxiliaryTerminationUnknown, detail)
			return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: detail}, ErrGenerationUnknown
		}
		observation, err := controller.Probe(ctx, identity)
		if err == nil && observation.State == pkgsandbox.ResourceStatePresent {
			observation, err = controller.Terminate(ctx, identity)
		}
		if err != nil || observation.State != pkgsandbox.ResourceStateAbsent {
			detail := observation.Detail
			if err != nil {
				detail = err.Error()
			}
			if detail == "" {
				detail = "controller did not prove preparation resource absence"
			}
			_, _ = s.markAuxiliaryTermination(ctx, row.AuxID, auxiliaryTerminationUnknown, detail)
			return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: detail}, err
		}
		if closeErr := aux.closeRaw(); closeErr != nil {
			detail := fmt.Sprintf("close preparation session after provider absence: %v", closeErr)
			_, _ = s.markAuxiliaryTermination(ctx, row.AuxID, auxiliaryTerminationUnknown, detail)
			return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: detail}, closeErr
		}
		if _, err := s.markAuxiliaryTermination(ctx, row.AuxID, auxiliaryTerminationAbsent, observation.Detail); err != nil {
			return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: err.Error()}, err
		}
		return observation, nil
	}

	if err := aux.closeRaw(); err != nil {
		detail := fmt.Sprintf("close controllerless preparation session: %v", err)
		_, _ = s.markAuxiliaryTermination(ctx, row.AuxID, auxiliaryTerminationUnknown, detail)
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: detail}, err
	}
	observer, ok := raw.(pkgsandbox.ResourceObservationProvider)
	if !ok {
		detail := "controllerless preparation backend cannot prove resource absence"
		_, _ = s.markAuxiliaryTermination(ctx, row.AuxID, auxiliaryTerminationUnknown, detail)
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: detail}, ErrGenerationNoControl
	}
	observation, err := observer.ObserveResource(ctx)
	if err != nil || observation.State != pkgsandbox.ResourceStateAbsent {
		detail := observation.Detail
		if err != nil {
			detail = err.Error()
		}
		if detail == "" {
			detail = "controllerless preparation resource absence is unknown"
		}
		_, _ = s.markAuxiliaryTermination(ctx, row.AuxID, auxiliaryTerminationUnknown, detail)
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: detail}, err
	}
	if _, err := s.markAuxiliaryTermination(ctx, row.AuxID, auxiliaryTerminationAbsent, observation.Detail); err != nil {
		return pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: err.Error()}, err
	}
	return observation, nil
}

func (s *GenerationStore) auxiliaryRows(ctx context.Context, key generationKey) ([]AuxiliaryRecord, error) {
	rows, err := s.q.ListSandboxGenerationAuxiliaries(ctx, sqlc.ListSandboxGenerationAuxiliariesParams{SessionID: key.sessionID, Generation: key.generation})
	if err != nil {
		return nil, err
	}
	result := make([]AuxiliaryRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, auxiliaryFromRow(row))
	}
	return result, nil
}

func (s *GenerationStore) withAuxiliaries(ctx context.Context, record GenerationRecord) (GenerationRecord, error) {
	rows, err := s.auxiliaryRows(ctx, generationKey{sessionID: record.SessionID, generation: record.Generation})
	if err != nil {
		return record, err
	}
	record.Auxiliaries = rows
	return record, nil
}

func (s *GenerationStore) auxiliaryAbsent(ctx context.Context, row GenerationRecord) (bool, string, error) {
	rows, err := s.auxiliaryRows(ctx, generationKey{sessionID: row.SessionID, generation: row.Generation})
	if err != nil {
		return false, "inspect auxiliary preparation ledger: " + err.Error(), err
	}
	for _, aux := range rows {
		if aux.TerminationState != auxiliaryTerminationAbsent {
			return false, fmt.Sprintf("auxiliary %s termination=%s", aux.AuxID, aux.TerminationState), nil
		}
	}
	return true, "", nil
}

// closeAuxiliaries drains every preparation resource that belongs to a
// generation before the generation's own provider resource is touched. The
// durable ledger is authoritative after a crash: when the in-process wrapper
// is gone, a controller can still prove absence from the persisted identity.
// A preparation with no identity and no local observer remains unknown and
// therefore blocks destruction of the parent generation.
func (s *GenerationStore) closeAuxiliaries(ctx context.Context, row GenerationRecord) error {
	key := generationKey{sessionID: row.SessionID, generation: row.Generation}
	var errs []error
	for _, aux := range s.auxiliaryEntriesFor(key) {
		if _, err := s.terminatePreparation(ctx, aux); err != nil {
			auxRow, _ := aux.metadata()
			errs = append(errs, fmt.Errorf("terminate sandbox auxiliary %s: %w", auxRow.AuxID, err))
		}
	}

	// A process restart removes the raw wrapper, but it does not remove the
	// durable auxiliary row. Retry controller-backed auxiliaries from their
	// persisted identity before declaring the parent cleanup unknown.
	rows, err := s.auxiliaryRows(ctx, key)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("inspect sandbox auxiliaries: %w", err))...)
	}
	for _, aux := range rows {
		if aux.TerminationState == auxiliaryTerminationAbsent {
			continue
		}
		if entry, ok := s.auxiliaryEntry(aux.AuxID); ok && entry.hasRaw() {
			// The in-process attempt above already recorded its latest state.
			// Do not race a second close against the same raw Session.
			continue
		}
		if err := s.reconcileAuxiliary(ctx, aux); err != nil {
			errs = append(errs, fmt.Errorf("reconcile sandbox auxiliary %s: %w", aux.AuxID, err))
		}
	}
	if absent, detail, checkErr := s.auxiliaryAbsent(ctx, row); checkErr != nil {
		errs = append(errs, checkErr)
	} else if !absent {
		errs = append(errs, fmt.Errorf("%w: %s", ErrGenerationUnknown, detail))
	}
	return errors.Join(errs...)
}

func (s *GenerationStore) auxiliaryEntry(auxID string) (*preparationRawSession, bool) {
	s.auxMu.Lock()
	aux, ok := s.auxEntries[auxID]
	s.auxMu.Unlock()
	return aux, ok
}

func (s *GenerationStore) reconcileAuxiliary(ctx context.Context, aux AuxiliaryRecord) error {
	identity := aux.Resource
	if identity.Backend == "" {
		identity.Backend = aux.Backend
	}
	if identity.Authority == "" || identity.Ref == "" {
		detail := "auxiliary resource identity is unavailable; manual recovery required"
		_, _ = s.markAuxiliaryTermination(ctx, aux.AuxID, auxiliaryTerminationUnknown, detail)
		return fmt.Errorf("%w: %s", ErrGenerationUnknown, detail)
	}
	controller, err := s.controller(ctx, aux.Backend)
	if err != nil {
		detail := fmt.Sprintf("construct auxiliary resource controller: %v", err)
		_, _ = s.markAuxiliaryTermination(ctx, aux.AuxID, auxiliaryTerminationUnknown, detail)
		return err
	}
	if controller == nil {
		detail := "auxiliary backend has no durable resource controller"
		_, _ = s.markAuxiliaryTermination(ctx, aux.AuxID, auxiliaryTerminationUnknown, detail)
		return fmt.Errorf("%w: %s", ErrGenerationNoControl, detail)
	}
	observation, err := controller.Probe(ctx, identity)
	if err == nil && observation.State == pkgsandbox.ResourceStatePresent {
		observation, err = controller.Terminate(ctx, identity)
	}
	if err != nil || observation.State != pkgsandbox.ResourceStateAbsent {
		detail := observation.Detail
		if err != nil {
			detail = err.Error()
		}
		if detail == "" {
			detail = "controller did not prove auxiliary resource absence"
		}
		_, _ = s.markAuxiliaryTermination(ctx, aux.AuxID, auxiliaryTerminationUnknown, detail)
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", ErrGenerationUnknown, detail)
	}
	if _, err := s.markAuxiliaryTermination(ctx, aux.AuxID, auxiliaryTerminationAbsent, observation.Detail); err != nil {
		return err
	}
	return nil
}

func (s *GenerationStore) auxiliaryEntriesFor(key generationKey) []*preparationRawSession {
	s.auxMu.Lock()
	entries := make([]*preparationRawSession, 0)
	for _, aux := range s.auxEntries {
		if aux.parent == key {
			entries = append(entries, aux)
		}
	}
	s.auxMu.Unlock()
	return entries
}

func (s *GenerationStore) snapshotAuxiliaryParents(sessionID string) []generationKey {
	s.auxMu.Lock()
	seen := make(map[generationKey]struct{}, len(s.auxEntries))
	for _, aux := range s.auxEntries {
		if sessionID == "" || aux.parent.sessionID == sessionID {
			seen[aux.parent] = struct{}{}
		}
	}
	s.auxMu.Unlock()
	keys := make([]generationKey, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	return keys
}

func (s *GenerationStore) forgetAuxiliaryEntries(key generationKey) {
	s.auxMu.Lock()
	for auxID, aux := range s.auxEntries {
		if aux.parent == key {
			delete(s.auxEntries, auxID)
		}
	}
	s.auxMu.Unlock()
}

func (s *GenerationStore) forgetAuxiliaryEntry(auxID string) {
	s.auxMu.Lock()
	delete(s.auxEntries, auxID)
	s.auxMu.Unlock()
}

// preparationRawSession is an in-process capability fence around a private
// preparation resource. It is kept only while termination is unproven; a
// restarted executor has no raw observer and must rely on controller proof or
// an explicit human acknowledgement.
type preparationRawSession struct {
	store     *GenerationStore
	parent    generationKey
	row       AuxiliaryRecord
	identity  pkgsandbox.ResourceIdentity
	raw       pkgsandbox.Session
	createErr error

	metaMu      sync.RWMutex
	lifecycleMu sync.Mutex

	closeMu  sync.Mutex
	closed   bool
	closeErr error

	uncertainMu  sync.RWMutex
	uncertainErr error
}

func (s *preparationRawSession) setCreationResult(raw pkgsandbox.Session, err error) {
	s.metaMu.Lock()
	s.raw = raw
	s.createErr = err
	s.metaMu.Unlock()
}

func (s *preparationRawSession) creationError() error {
	s.metaMu.RLock()
	err := s.createErr
	s.metaMu.RUnlock()
	return err
}

func (s *preparationRawSession) rawSession() pkgsandbox.Session {
	s.metaMu.RLock()
	raw := s.raw
	s.metaMu.RUnlock()
	return raw
}

func (s *preparationRawSession) hasRaw() bool { return s.rawSession() != nil }

func (s *preparationRawSession) metadata() (AuxiliaryRecord, pkgsandbox.ResourceIdentity) {
	s.metaMu.RLock()
	row, identity := s.row, s.identity
	s.metaMu.RUnlock()
	return row, identity
}

func (s *preparationRawSession) setIdentity(identity pkgsandbox.ResourceIdentity) {
	s.metaMu.Lock()
	s.identity = identity
	s.metaMu.Unlock()
}

func (s *preparationRawSession) setRecord(row AuxiliaryRecord) {
	s.metaMu.Lock()
	s.row = row
	s.metaMu.Unlock()
}

func (s *preparationRawSession) markUncertain(err error) {
	if err == nil {
		return
	}
	s.uncertainMu.Lock()
	if s.uncertainErr == nil {
		s.uncertainErr = err
	}
	s.uncertainMu.Unlock()
}

func (s *preparationRawSession) uncertain() error {
	s.uncertainMu.RLock()
	err := s.uncertainErr
	s.uncertainMu.RUnlock()
	return err
}

func (s *preparationRawSession) parentRecord(ctx context.Context) GenerationRecord {
	row, err := s.store.q.GetSandboxGeneration(ctx, sqlc.GetSandboxGenerationParams{SessionID: s.parent.sessionID, Generation: s.parent.generation})
	if err != nil {
		auxRow, _ := s.metadata()
		return GenerationRecord{SessionID: s.parent.sessionID, Generation: s.parent.generation, OwnerBootID: s.store.ownerBootID, Backend: auxRow.Backend, State: GenerationActive}
	}
	return recordFromRow(row)
}

func (s *preparationRawSession) operationError(detail string, err error) {
	if err == nil || isNotStarted(err) {
		return
	}
	s.markUncertain(err)
	row, _ := s.metadata()
	_, _ = s.store.markAuxiliaryExecution(context.Background(), row.AuxID, auxiliaryExecutionUnknown, detail)
	s.store.markPreparationParentUnknown(context.Background(), s.parentRecord(context.Background()), detail)
}

func (s *preparationRawSession) guard(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.uncertain(); err != nil {
		return fmt.Errorf("%w: auxiliary preparation outcome is uncertain: %w", ErrGenerationUnknown, err)
	}
	if err := agentrun.Check(ctx); err != nil {
		return err
	}
	raw := s.rawSession()
	if raw == nil || !raw.Alive() {
		return ErrGenerationUnknown
	}
	row, err := s.store.q.GetSandboxGeneration(ctx, sqlc.GetSandboxGenerationParams{SessionID: s.parent.sessionID, Generation: s.parent.generation})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGenerationNotFound
	}
	if err != nil {
		return err
	}
	if row.OwnerBootID != s.store.ownerBootID {
		return ErrGenerationBusy
	}
	state := GenerationState(row.State)
	if state != GenerationCreating && state != GenerationActive {
		return fmt.Errorf("%w: preparation parent state=%s", ErrGenerationFenced, state)
	}
	return nil
}

func (s *preparationRawSession) closeRaw() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return s.closeErr
	}
	raw := s.rawSession()
	if raw == nil {
		err := s.creationError()
		if err == nil {
			err = ErrGenerationUnknown
		}
		s.closeErr = err
		return err
	}
	err := raw.Close()
	s.closeErr = err
	if err == nil {
		s.closed = true
	}
	return err
}

func (s *preparationRawSession) Policy() pkgsandbox.Policy {
	if err := s.guard(context.Background()); err != nil {
		return pkgsandbox.Policy{}
	}
	return s.rawSession().Policy()
}

func (s *preparationRawSession) WorkingDir() string {
	if err := s.guard(context.Background()); err != nil {
		return ""
	}
	return s.rawSession().WorkingDir()
}

func (s *preparationRawSession) Alive() bool {
	raw := s.rawSession()
	return s.uncertain() == nil && raw != nil && raw.Alive()
}

func (s *preparationRawSession) Done() <-chan struct{} {
	raw := s.rawSession()
	if raw != nil {
		return raw.Done()
	}
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *preparationRawSession) Close() error { return s.closeRaw() }

func (s *preparationRawSession) Exec(ctx context.Context, command string, opts pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	if err := s.guard(ctx); err != nil {
		return pkgsandbox.ExecResult{}, err
	}
	result, err := s.rawSession().Exec(ctx, command, opts)
	if err != nil {
		s.operationError(fmt.Sprintf("auxiliary exec: %v", err), err)
	}
	return result, err
}

func (s *preparationRawSession) StartProcess(ctx context.Context, req pkgsandbox.ProcessRequest) (pkgsandbox.ProcessHandle, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	handle, err := s.rawSession().StartProcess(ctx, req)
	if err != nil {
		s.operationError(fmt.Sprintf("auxiliary start process: %v", err), err)
		return nil, err
	}
	if handle == nil {
		err := errors.New("sandbox: preparation backend returned nil process handle")
		s.operationError(err.Error(), err)
		return nil, err
	}
	return &preparationProcessHandle{owner: s, inner: handle, ctx: ctx}, nil
}

func (s *preparationRawSession) Files() pkgsandbox.FileAccess {
	return &preparationFileAccess{owner: s, inner: s.rawSession().Files(), ctx: context.Background()}
}

func (s *preparationRawSession) ResourceIdentity(ctx context.Context) (pkgsandbox.ResourceIdentity, error) {
	if err := s.guard(ctx); err != nil {
		return pkgsandbox.ResourceIdentity{}, err
	}
	_, identity := s.metadata()
	return identity, nil
}

func (s *preparationRawSession) RenderEnv(ctx context.Context, env map[string]string) (map[string]string, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	return pkgsandbox.RenderEnv(ctx, s.rawSession(), env)
}

func (s *preparationRawSession) Sync() error {
	if err := s.guard(context.Background()); err != nil {
		return err
	}
	if syncer, ok := s.rawSession().(interface{ Sync() error }); ok {
		return syncer.Sync()
	}
	return nil
}

type preparationProcessHandle struct {
	owner *preparationRawSession
	inner pkgsandbox.ProcessHandle
	ctx   context.Context
}

func (h *preparationProcessHandle) PID() int { return h.inner.PID() }
func (h *preparationProcessHandle) Stdin() io.WriteCloser {
	return &preparationStdin{owner: h.owner, inner: h.inner.Stdin(), ctx: h.ctx}
}
func (h *preparationProcessHandle) Stdout() io.ReadCloser { return h.inner.Stdout() }
func (h *preparationProcessHandle) Stderr() io.ReadCloser { return h.inner.Stderr() }
func (h *preparationProcessHandle) Wait(ctx context.Context) (pkgsandbox.ExecResult, error) {
	if err := h.owner.guard(ctx); err != nil {
		return pkgsandbox.ExecResult{}, err
	}
	result, err := h.inner.Wait(ctx)
	if err != nil {
		h.owner.operationError(fmt.Sprintf("auxiliary wait process: %v", err), err)
	}
	return result, err
}

func (h *preparationProcessHandle) Close() error {
	if err := h.owner.guard(context.Background()); err != nil {
		return err
	}
	return h.inner.Close()
}

type preparationStdin struct {
	owner *preparationRawSession
	inner io.WriteCloser
	ctx   context.Context
}

func (w *preparationStdin) Write(p []byte) (int, error) {
	if err := w.owner.guard(w.ctx); err != nil {
		return 0, err
	}
	if w.inner == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := w.inner.Write(p)
	if err != nil {
		w.owner.operationError(fmt.Sprintf("auxiliary stdin write: %v", err), err)
	}
	return n, err
}

func (w *preparationStdin) Close() error {
	if w.inner == nil {
		return nil
	}
	return w.inner.Close()
}

type preparationFileAccess struct {
	owner *preparationRawSession
	inner pkgsandbox.FileAccess
	ctx   context.Context
}

func (f *preparationFileAccess) check() error {
	ctx := f.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return f.owner.guard(ctx)
}

func (f *preparationFileAccess) ReadFile(path string) ([]byte, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	return f.inner.ReadFile(path)
}

func (f *preparationFileAccess) ReadDir(path string) ([]pkgsandbox.DirEntry, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	return f.inner.ReadDir(path)
}

func (f *preparationFileAccess) Stat(path string) (pkgsandbox.FileInfo, error) {
	if err := f.check(); err != nil {
		return pkgsandbox.FileInfo{}, err
	}
	return f.inner.Stat(path)
}

func (f *preparationFileAccess) WriteFile(path string, content []byte, mode fs.FileMode) error {
	if err := f.check(); err != nil {
		return err
	}
	err := f.inner.WriteFile(path, content, mode)
	if err != nil {
		f.owner.operationError(fmt.Sprintf("auxiliary file write: %v", err), err)
	}
	return err
}

func (f *preparationFileAccess) ProjectFiles(path string, files []pkgsandbox.ProjectedFile) error {
	if err := f.check(); err != nil {
		return err
	}
	err := f.inner.ProjectFiles(path, files)
	if err != nil {
		f.owner.operationError(fmt.Sprintf("auxiliary file projection: %v", err), err)
	}
	return err
}

func (f *preparationFileAccess) ProjectTempFiles(path string, files []pkgsandbox.ProjectedFile) (string, error) {
	if err := f.check(); err != nil {
		return "", err
	}
	result, err := f.inner.ProjectTempFiles(path, files)
	if err != nil {
		f.owner.operationError(fmt.Sprintf("auxiliary temp projection: %v", err), err)
	}
	return result, err
}

var _ pkgsandbox.Session = (*preparationRawSession)(nil)
