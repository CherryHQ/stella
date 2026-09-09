package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// GenerationState is the durable lifecycle of one disposable compute
// generation. Unknown and fenced are intentionally different from destroyed:
// neither state is a proof that a resource is absent.
type GenerationState string

const (
	GenerationCreating  GenerationState = "creating"
	GenerationActive    GenerationState = "active"
	GenerationFenced    GenerationState = "fenced"
	GenerationUnknown   GenerationState = "unknown"
	GenerationDestroyed GenerationState = "destroyed"

	// MaxRetainedGenerations is a deliberate memory ceiling. Durable rows are
	// retained until their resource has an absence proof, and only idle proved
	// rows can be evicted when this ceiling is reached.
	MaxRetainedGenerations = 1024
)

var (
	ErrGenerationBusy      = errors.New("sandbox generation is already owned")
	ErrGenerationUnknown   = errors.New("sandbox generation resource outcome is unknown")
	ErrGenerationFenced    = errors.New("sandbox generation is fenced")
	ErrGenerationNotFound  = errors.New("sandbox generation not found")
	ErrGenerationCapacity  = errors.New("sandbox generation retention capacity exhausted")
	ErrGenerationNoControl = errors.New("sandbox backend has no durable resource controller")
	ErrGenerationAckDenied = errors.New("sandbox generation absence acknowledgement rejected")
)

// GenerationRecord is the operator-facing durable row. Resource identity is
// opaque and never doubles as a Workspace path.
type GenerationRecord struct {
	SessionID    string                      `json:"session_id"`
	Generation   int64                       `json:"generation"`
	OwnerBootID  string                      `json:"owner_boot_id"`
	Backend      string                      `json:"backend"`
	ConfigDigest string                      `json:"config_digest"`
	State        GenerationState             `json:"state"`
	Resource     pkgsandbox.ResourceIdentity `json:"resource"`
	Auxiliaries  []AuxiliaryRecord           `json:"auxiliaries,omitempty"`
	LastError    string                      `json:"last_error"`
	CreatedAt    time.Time                   `json:"created_at"`
	StartedAt    time.Time                   `json:"started_at"`
	ActiveAt     *time.Time                  `json:"active_at,omitempty"`
	FencedAt     *time.Time                  `json:"fenced_at,omitempty"`
	DestroyedAt  *time.Time                  `json:"destroyed_at,omitempty"`
	UpdatedAt    time.Time                   `json:"updated_at"`
}

func recordFromRow(row sqlc.AgentSandboxGeneration) GenerationRecord {
	return GenerationRecord{
		SessionID:    row.SessionID,
		Generation:   row.Generation,
		OwnerBootID:  row.OwnerBootID,
		Backend:      row.Backend,
		ConfigDigest: row.ConfigDigest,
		State:        GenerationState(row.State),
		Resource: pkgsandbox.ResourceIdentity{
			Backend:   row.Backend,
			Authority: row.ResourceAuthority,
			Ref:       row.ResourceRef,
		},
		LastError:   row.LastError,
		CreatedAt:   row.CreatedAt.UTC(),
		StartedAt:   row.StartedAt.UTC(),
		ActiveAt:    nullableTime(row.ActiveAt),
		FencedAt:    nullableTime(row.FencedAt),
		DestroyedAt: nullableTime(row.DestroyedAt),
		UpdatedAt:   row.UpdatedAt.UTC(),
	}
}

func recordFromCreateRow(row sqlc.CreateSandboxGenerationRow) GenerationRecord {
	return GenerationRecord{
		SessionID:    row.SessionID,
		Generation:   row.Generation,
		OwnerBootID:  row.OwnerBootID,
		Backend:      row.Backend,
		ConfigDigest: row.ConfigDigest,
		State:        GenerationState(row.State),
		Resource: pkgsandbox.ResourceIdentity{
			Backend:   row.Backend,
			Authority: row.ResourceAuthority,
			Ref:       row.ResourceRef,
		},
		LastError:   row.LastError,
		CreatedAt:   row.CreatedAt.UTC(),
		StartedAt:   row.StartedAt.UTC(),
		ActiveAt:    nullableTime(row.ActiveAt),
		FencedAt:    nullableTime(row.FencedAt),
		DestroyedAt: nullableTime(row.DestroyedAt),
		UpdatedAt:   row.UpdatedAt.UTC(),
	}
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

type generationKey struct {
	sessionID  string
	generation int64
}

type generationEntry struct {
	key       generationKey
	row       GenerationRecord
	raw       *generationRawSession
	refs      int
	ownerHeld bool
}

// GenerationStore owns raw sessions for one executor boot and coordinates
// durable state transitions. It is safe for concurrent runner/cache callers.
type GenerationStore struct {
	q           *sqlc.Queries
	db          sqlc.DBTX
	ownerBootID string
	backends    *BackendRegistry
	maxRetained int
	mu          sync.Mutex
	entries     map[generationKey]*generationEntry
	creating    int
	auxMu       sync.Mutex
	auxEntries  map[string]*preparationRawSession
}

// NewGenerationStore creates the process-local owner around the PostgreSQL
// SessionSandbox state. ownerBootID must be the same identity registered by
// agentrun.Store for this stellad process.
func NewGenerationStore(db sqlc.DBTX, ownerBootID string, backends *BackendRegistry) *GenerationStore {
	return &GenerationStore{
		q:           sqlc.New(db),
		db:          db,
		ownerBootID: ownerBootID,
		backends:    backends,
		maxRetained: MaxRetainedGenerations,
		entries:     make(map[generationKey]*generationEntry),
		auxEntries:  make(map[string]*preparationRawSession),
	}
}

// GenerationAdmin is the operator-facing, non-runner view of durable sandbox
// generations. It has no process-local owner boot and never opens a Session;
// Reconcile constructs a controller only when explicitly requested.
type GenerationAdmin struct {
	store *GenerationStore
}

// NewGenerationAdmin creates the maintenance API over an already initialized
// database. It does not run migrations or register an executor boot.
func NewGenerationAdmin(db sqlc.DBTX, backends *BackendRegistry) *GenerationAdmin {
	return &GenerationAdmin{store: NewGenerationStore(db, "", backends)}
}

func (a *GenerationAdmin) Inspect(ctx context.Context, sessionID string) (GenerationRecord, error) {
	if a == nil || a.store == nil {
		return GenerationRecord{}, ErrGenerationNotFound
	}
	return a.store.Inspect(ctx, sessionID)
}

func (a *GenerationAdmin) Reconcile(ctx context.Context, sessionID string, generation int64) (GenerationRecord, pkgsandbox.ResourceObservation, error) {
	if a == nil || a.store == nil {
		return GenerationRecord{}, pkgsandbox.ResourceObservation{}, ErrGenerationNotFound
	}
	return a.store.Reconcile(ctx, sessionID, generation)
}

func (a *GenerationAdmin) AcknowledgeAbsent(ctx context.Context, sessionID string, generation int64, ownerBootID, reason string, force bool) (GenerationRecord, error) {
	if a == nil || a.store == nil {
		return GenerationRecord{}, ErrGenerationAckDenied
	}
	return a.store.AcknowledgeAbsent(ctx, sessionID, generation, ownerBootID, reason, force)
}

// OwnerBootID returns the immutable executor identity used by this store.
func (s *GenerationStore) OwnerBootID() string {
	if s == nil {
		return ""
	}
	return s.ownerBootID
}

// GenerationSpec describes one Session's backend-independent creation plan.
// The callback receives a fresh monotonic generation and must pass both values
// into every backend resource it creates.
type GenerationSpec struct {
	SessionID    string
	Backend      string
	ConfigDigest string
	Create       func(context.Context, int64, string) (pkgsandbox.Session, error)
}

// Open creates a retained, guarded resilient Session. If the current durable
// generation is still present or unknown, Open refuses to create a successor;
// the caller must reconcile or explicitly acknowledge absence first.
func (s *GenerationStore) Open(ctx context.Context, spec GenerationSpec) (pkgsandbox.Session, error) {
	if s == nil {
		return nil, errors.New("sandbox: generation store is required")
	}
	if spec.SessionID == "" || spec.Backend == "" || spec.ConfigDigest == "" || spec.Create == nil {
		return nil, errors.New("sandbox: incomplete generation specification")
	}
	if s.ownerBootID == "" {
		return nil, errors.New("sandbox: generation owner boot is required")
	}
	managed := &generationSession{store: s, spec: spec}
	if existing, ok, err := s.openExisting(ctx, spec, managed); err != nil {
		return nil, err
	} else if ok {
		managed.inner = pkgsandbox.NewResilientSession(existing, managed.create)
		return managed, nil
	}
	raw, err := managed.create(ctx)
	if err != nil {
		return nil, err
	}
	managed.inner = pkgsandbox.NewResilientSession(raw, managed.create)
	return managed, nil
}

// openExisting borrows the live generation retained by this executor. Idle
// runner eviction releases only this borrow, so a later runner for the same
// Session can reopen the exact generation without creating a second resource.
// A durable row from another boot is never attachable to this process.
func (s *GenerationStore) openExisting(ctx context.Context, spec GenerationSpec, owner *generationSession) (pkgsandbox.Session, bool, error) {
	row, err := s.q.GetCurrentSandboxGeneration(ctx, spec.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect sandbox generation: %w", err)
	}
	if GenerationState(row.State) != GenerationActive || row.OwnerBootID != s.ownerBootID || row.Backend != spec.Backend || row.ConfigDigest != spec.ConfigDigest {
		return nil, false, nil
	}
	key := generationKey{sessionID: row.SessionID, generation: row.Generation}
	s.mu.Lock()
	entry := s.entries[key]
	if entry == nil || entry.raw == nil || entry.row.State != GenerationActive {
		s.mu.Unlock()
		return nil, false, nil
	}
	entry.refs++
	raw := entry.raw
	s.mu.Unlock()
	if !raw.localAlive() {
		s.release(key)
		return nil, false, nil
	}
	if err := raw.uncertain(); err != nil {
		s.release(key)
		if errors.Is(err, ErrGenerationBusy) {
			// A busy marker is an ownership conflict, not a dead raw that this
			// store may reconcile into a successor.
			return nil, false, err
		}
		// The local fence is shared by all borrowers, but the durable row may
		// still be active when the uncertain operation could not update the DB.
		// Let begin() observe entryAlive=false and perform the normal durable
		// fence/controller proof before allocating a successor. Existing
		// borrowers continue to fail closed through selected().
		return nil, false, nil
	}
	if err := raw.guard(ctx); err != nil {
		s.release(key)
		return nil, false, err
	}
	owner.setCurrent(raw)
	owner.setBorrowKey(key)
	return raw, true, nil
}

func (s *GenerationStore) begin(ctx context.Context, spec GenerationSpec) (GenerationRecord, error) {
	current, err := s.q.GetCurrentSandboxGeneration(ctx, spec.SessionID)
	if err == nil && GenerationState(current.State) != GenerationDestroyed {
		key := generationKey{sessionID: current.SessionID, generation: current.Generation}
		if s.entryAlive(key, GenerationState(current.State), spec.Backend, spec.ConfigDigest) {
			return GenerationRecord{}, fmt.Errorf("%w: session %q", ErrGenerationBusy, spec.SessionID)
		}
		if current.OwnerBootID != s.ownerBootID {
			recoverable, recoveryErr := s.ownerRecoverable(ctx, current.OwnerBootID)
			if recoveryErr != nil {
				return GenerationRecord{}, recoveryErr
			}
			if !recoverable {
				return GenerationRecord{}, fmt.Errorf("%w: generation %d is owned by live executor %s", ErrGenerationBusy, current.Generation, current.OwnerBootID)
			}
		}
		if err := s.reconcileForReplacement(ctx, recordFromRow(current)); err != nil {
			return GenerationRecord{}, err
		}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return GenerationRecord{}, fmt.Errorf("inspect sandbox generation: %w", err)
	}

	row, err := s.createLocked(ctx, spec)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GenerationRecord{}, fmt.Errorf("%w: session %q", ErrGenerationBusy, spec.SessionID)
		}
		return GenerationRecord{}, fmt.Errorf("create sandbox generation: %w", err)
	}
	record := recordFromCreateRow(row)
	s.mu.Lock()
	s.entries[generationKey{sessionID: record.SessionID, generation: record.Generation}] = &generationEntry{key: generationKey{sessionID: record.SessionID, generation: record.Generation}, row: record, ownerHeld: true}
	s.mu.Unlock()
	return record, nil
}

func (s *GenerationStore) ownerRecoverable(ctx context.Context, bootID string) (bool, error) {
	state, err := s.q.GetExecutorBootRecoveryState(ctx, bootID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("sandbox generation owner boot %s disappeared", bootID)
	}
	if err != nil {
		return false, fmt.Errorf("inspect sandbox generation owner boot: %w", err)
	}
	if state.Status == "drained" {
		return true, nil
	}
	return state.HeartbeatAt.UTC().Before(time.Now().UTC().Add(-30 * time.Second)), nil
}

func (s *GenerationStore) openRaw(ctx context.Context, spec GenerationSpec, owner *generationSession) (pkgsandbox.Session, error) {
	releaseReservation, err := s.reserveCapacity(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseReservation()

	record, err := s.begin(ctx, spec)
	if err != nil {
		return nil, err
	}
	raw, err := spec.Create(ctx, record.Generation, s.ownerBootID)
	if err != nil || raw == nil {
		detail := "backend creation outcome is unknown"
		if err != nil {
			detail = fmt.Sprintf("backend creation outcome is unknown: %v", err)
		}
		s.markUnknown(ctx, record, detail)
		if err == nil {
			err = errors.New("backend returned a nil sandbox session")
		}
		return nil, fmt.Errorf("sandbox generation %d: %w", record.Generation, err)
	}

	identity := pkgsandbox.ResourceIdentity{Backend: record.Backend}
	if provider, ok := raw.(pkgsandbox.ResourceIdentityProvider); ok {
		identity, err = provider.ResourceIdentity(ctx)
		if err != nil {
			if _, transitionErr := s.markUnknownChecked(ctx, record, fmt.Sprintf("read resource identity: %v", err)); transitionErr == nil {
				_ = raw.Close()
			}
			return nil, fmt.Errorf("sandbox generation %d identity: %w", record.Generation, err)
		}
		if identity.Backend == "" {
			identity.Backend = record.Backend
		}
	}
	if s.backends != nil && s.backends.HasController(record.Backend) && (identity.Authority == "" || identity.Ref == "") {
		if _, transitionErr := s.markUnknownChecked(ctx, record, "backend controller requires a durable resource identity"); transitionErr == nil {
			_ = raw.Close()
		}
		return nil, fmt.Errorf("sandbox generation %d: %w", record.Generation, ErrGenerationUnknown)
	}
	active, err := s.q.MarkSandboxGenerationActive(ctx, sqlc.MarkSandboxGenerationActiveParams{
		ResourceAuthority: identity.Authority, ResourceRef: identity.Ref,
		SessionID: record.SessionID, Generation: record.Generation, OwnerBootID: record.OwnerBootID,
	})
	if err != nil {
		if _, transitionErr := s.markUnknownChecked(ctx, record, fmt.Sprintf("activate generation: %v", err)); transitionErr == nil {
			_ = raw.Close()
		}
		return nil, fmt.Errorf("activate sandbox generation %d: %w", record.Generation, err)
	}
	activeRecord := recordFromRow(active)
	key := generationKey{sessionID: record.SessionID, generation: record.Generation}
	guarded := &generationRawSession{store: s, key: key, row: activeRecord, raw: raw, identity: identity}
	s.mu.Lock()
	entry := s.entries[key]
	if entry == nil {
		entry = &generationEntry{key: guarded.key, ownerHeld: true}
		s.entries[key] = entry
	}
	entry.row = activeRecord
	entry.raw = guarded
	entry.refs++
	s.mu.Unlock()
	if owner != nil {
		owner.setCurrent(guarded)
		oldKey, hadOld := owner.replaceBorrowKey(key)
		if hadOld && oldKey != key {
			s.release(oldKey)
		}
	}
	return guarded, nil
}

func (s *GenerationStore) guard(ctx context.Context, key generationKey, raw pkgsandbox.Session) (GenerationRecord, error) {
	if raw == nil || !raw.Alive() {
		return GenerationRecord{}, ErrGenerationUnknown
	}
	row, err := s.q.GetSandboxGeneration(ctx, sqlc.GetSandboxGenerationParams{SessionID: key.sessionID, Generation: key.generation})
	if errors.Is(err, pgx.ErrNoRows) {
		return GenerationRecord{}, ErrGenerationNotFound
	}
	if err != nil {
		return GenerationRecord{}, err
	}
	record := recordFromRow(row)
	if record.OwnerBootID != s.ownerBootID || record.State != GenerationActive {
		return record, fmt.Errorf("%w: generation %d state=%s", ErrGenerationFenced, key.generation, record.State)
	}
	return record, nil
}

// createLocked takes a transaction-scoped advisory lock in a separate
// statement, then calculates MAX/NOT EXISTS under a fresh READ COMMITTED
// snapshot. Keeping lock and insert in one CTE would let a waiter retain its
// pre-lock snapshot and race the previous generation.
func (s *GenerationStore) createLocked(ctx context.Context, spec GenerationSpec) (sqlc.CreateSandboxGenerationRow, error) {
	type beginner interface {
		Begin(context.Context) (pgx.Tx, error)
	}
	db, ok := s.db.(beginner)
	if !ok {
		return sqlc.CreateSandboxGenerationRow{}, errors.New("sandbox generation requires a transactional database")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return sqlc.CreateSandboxGenerationRow{}, err
	}
	qtx := s.q.WithTx(tx)
	if err := qtx.LockSandboxGenerationSession(ctx, spec.SessionID); err != nil {
		_ = tx.Rollback(ctx)
		return sqlc.CreateSandboxGenerationRow{}, err
	}
	row, err := qtx.CreateSandboxGeneration(ctx, sqlc.CreateSandboxGenerationParams{
		SessionID: spec.SessionID, OwnerBootID: s.ownerBootID,
		Backend: spec.Backend, ConfigDigest: spec.ConfigDigest,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return sqlc.CreateSandboxGenerationRow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.CreateSandboxGenerationRow{}, err
	}
	return row, nil
}

// reserveCapacity reserves one process-local retention slot for a generation
// creation. The reservation is made before begin inserts its entry, so
// concurrent Sessions cannot each observe the same last free slot. It also
// covers resilient recreation, which enters through openRaw rather than Open.
// Ceiling: creation is globally serialized only at the retention boundary;
// raise maxRetained or add per-backend quotas if creation throughput matters.
func (s *GenerationStore) reserveCapacity(ctx context.Context) (func(), error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	s.mu.Lock()
	for len(s.entries)+s.creating >= s.maxRetained {
		var evict generationKey
		found := false
		for key, entry := range s.entries {
			if entry.refs == 0 && !entry.ownerHeld && entry.row.State == GenerationDestroyed {
				evict = key
				found = true
				break
			}
		}
		if !found {
			s.mu.Unlock()
			return nil, ErrGenerationCapacity
		}
		// Durable history is never deleted here. Removing only the
		// process-local owner/borrow entry preserves monotonic MAX(generation)
		// across restarts.
		delete(s.entries, evict)
	}
	s.creating++
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.creating > 0 {
				s.creating--
			}
			s.mu.Unlock()
		})
	}, nil
}

func (s *GenerationStore) entryAlive(key generationKey, state GenerationState, backend, configDigest string) bool {
	if state != GenerationActive {
		return false
	}
	s.mu.Lock()
	entry := s.entries[key]
	if entry == nil || entry.raw == nil {
		s.mu.Unlock()
		return false
	}
	entryState := entry.row.State
	entryBackend := entry.row.Backend
	entryDigest := entry.row.ConfigDigest
	raw := entry.raw
	s.mu.Unlock()
	return entryState == GenerationActive && entryBackend == backend && entryDigest == configDigest && raw.localAlive() && raw.uncertain() == nil
}

func (s *GenerationStore) controller(ctx context.Context, backend string) (pkgsandbox.ResourceController, error) {
	s.mu.Lock()
	backends := s.backends
	s.mu.Unlock()
	if backends == nil {
		return nil, nil
	}
	return backends.Controller(ctx, backend)
}

func (s *GenerationStore) markUnknown(ctx context.Context, row GenerationRecord, detail string) GenerationRecord {
	record, err := s.markUnknownChecked(ctx, row, detail)
	if err == nil {
		return record
	}
	return row
}

func (s *GenerationStore) markUnknownChecked(ctx context.Context, row GenerationRecord, detail string) (GenerationRecord, error) {
	updated, err := s.q.MarkSandboxGenerationUnknown(ctx, sqlc.MarkSandboxGenerationUnknownParams{
		SessionID: row.SessionID, Generation: row.Generation, LastError: detail,
	})
	if err == nil {
		record := recordFromRow(updated)
		s.updateEntry(record)
		return record, nil
	}
	return row, err
}

func (s *GenerationStore) markFenced(ctx context.Context, row GenerationRecord, detail string) (GenerationRecord, error) {
	updated, err := s.q.MarkSandboxGenerationFenced(ctx, sqlc.MarkSandboxGenerationFencedParams{
		SessionID: row.SessionID, Generation: row.Generation, LastError: detail,
	})
	if err == nil {
		record := recordFromRow(updated)
		s.updateEntry(record)
		return record, nil
	}
	return row, err
}

func (s *GenerationStore) updateEntry(row GenerationRecord) {
	s.mu.Lock()
	if entry := s.entries[generationKey{sessionID: row.SessionID, generation: row.Generation}]; entry != nil {
		entry.row = row
		if row.State == GenerationDestroyed {
			// Durable reconciliation or an explicit acknowledgement has proved
			// absence, so no provider owner remains even if this update came from
			// a path other than raw.Close. Keep refs until borrowers release.
			entry.ownerHeld = false
		}
	}
	s.mu.Unlock()
}

func (s *GenerationStore) reconcileForReplacement(ctx context.Context, row GenerationRecord) error {
	if row.State == GenerationDestroyed {
		return nil
	}
	if err := s.closeAuxiliaries(ctx, row); err != nil {
		s.markUnknown(ctx, row, fmt.Sprintf("reconcile auxiliary preparation: %v", err))
		return err
	}
	if row.Resource.Authority == "" || row.Resource.Ref == "" {
		s.markUnknown(ctx, row, "resource identity is unavailable; manual recovery required")
		return fmt.Errorf("%w: generation %d has no durable resource identity", ErrGenerationUnknown, row.Generation)
	}
	controller, err := s.controller(ctx, row.Backend)
	if err != nil {
		s.markUnknown(ctx, row, fmt.Sprintf("construct resource controller: %v", err))
		return fmt.Errorf("%w: construct resource controller: %w", ErrGenerationUnknown, err)
	}
	if controller == nil {
		s.markUnknown(ctx, row, "backend has no durable resource controller")
		return fmt.Errorf("%w: %s", ErrGenerationNoControl, row.Backend)
	}
	if row.State != GenerationFenced && row.State != GenerationUnknown {
		var err error
		row, err = s.markFenced(ctx, row, "replacement requires proof of prior-resource absence")
		if err != nil {
			return fmt.Errorf("fence sandbox generation before reconciliation: %w", err)
		}
	}
	identity := row.Resource
	if identity.Backend == "" {
		identity.Backend = row.Backend
	}
	observation, err := controller.Probe(ctx, identity)
	if err != nil {
		s.markUnknown(ctx, row, fmt.Sprintf("probe resource: %v", err))
		return fmt.Errorf("%w: probe resource: %w", ErrGenerationUnknown, err)
	}
	if observation.State == pkgsandbox.ResourceStateAbsent {
		updated, updateErr := s.q.MarkSandboxGenerationReconciledAbsent(ctx, sqlc.MarkSandboxGenerationReconciledAbsentParams{
			SessionID: row.SessionID, Generation: row.Generation, LastError: observation.Detail,
		})
		if updateErr == nil {
			s.updateEntry(recordFromRow(updated))
			return nil
		}
		return fmt.Errorf("mark absent sandbox generation: %w", updateErr)
	}
	if observation.State == pkgsandbox.ResourceStatePresent {
		terminated, terminateErr := controller.Terminate(ctx, identity)
		if terminateErr != nil {
			s.markUnknown(ctx, row, fmt.Sprintf("terminate resource: %v", terminateErr))
			return fmt.Errorf("%w: terminate resource: %w", ErrGenerationUnknown, terminateErr)
		}
		if terminated.State == pkgsandbox.ResourceStateAbsent {
			updated, updateErr := s.q.MarkSandboxGenerationReconciledAbsent(ctx, sqlc.MarkSandboxGenerationReconciledAbsentParams{
				SessionID: row.SessionID, Generation: row.Generation, LastError: terminated.Detail,
			})
			if updateErr == nil {
				s.updateEntry(recordFromRow(updated))
				return nil
			}
			return fmt.Errorf("mark terminated sandbox generation: %w", updateErr)
		}
		observation = terminated
	}
	s.markUnknown(ctx, row, observation.Detail)
	return fmt.Errorf("%w: resource state %s", ErrGenerationUnknown, observation.State)
}

// Inspect returns the latest monotonic generation for a Session.
func (s *GenerationStore) Inspect(ctx context.Context, sessionID string) (GenerationRecord, error) {
	if s == nil || s.q == nil {
		return GenerationRecord{}, ErrGenerationNotFound
	}
	row, err := s.q.GetCurrentSandboxGeneration(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return GenerationRecord{}, ErrGenerationNotFound
	}
	if err != nil {
		return GenerationRecord{}, err
	}
	record, err := s.withAuxiliaries(ctx, recordFromRow(row))
	if err != nil {
		return GenerationRecord{}, err
	}
	return record, nil
}

// Reconcile probes and, when present, terminates the exact durable resource.
// It refuses active generations so an operator cannot accidentally destroy a
// healthy owner. The caller must first fence an uncertain generation through
// executor recovery or an explicit lifecycle action.
func (s *GenerationStore) Reconcile(ctx context.Context, sessionID string, generation int64) (GenerationRecord, pkgsandbox.ResourceObservation, error) {
	row, err := s.q.GetSandboxGeneration(ctx, sqlc.GetSandboxGenerationParams{SessionID: sessionID, Generation: generation})
	if errors.Is(err, pgx.ErrNoRows) {
		return GenerationRecord{}, pkgsandbox.ResourceObservation{}, ErrGenerationNotFound
	}
	if err != nil {
		return GenerationRecord{}, pkgsandbox.ResourceObservation{}, err
	}
	record := recordFromRow(row)
	record, err = s.withAuxiliaries(ctx, record)
	if err != nil {
		return record, pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: err.Error()}, err
	}
	if record.State == GenerationDestroyed {
		return record, pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateAbsent, Detail: "generation already destroyed"}, nil
	}
	if record.State != GenerationFenced && record.State != GenerationUnknown {
		return record, pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: "generation is not fenced"}, ErrGenerationFenced
	}
	if err := s.closeAuxiliaries(ctx, record); err != nil {
		return record, pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: err.Error()}, err
	}
	if record, err = s.withAuxiliaries(ctx, record); err != nil {
		return record, pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: err.Error()}, err
	}
	if record.Resource.Authority == "" || record.Resource.Ref == "" {
		s.markUnknown(ctx, record, "resource identity is unavailable; manual recovery required")
		return record, pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: "resource identity is unavailable"}, ErrGenerationUnknown
	}
	controller, err := s.controller(ctx, record.Backend)
	if err != nil {
		return record, pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: err.Error()}, err
	}
	if controller == nil {
		return record, pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: "backend has no durable resource controller"}, ErrGenerationNoControl
	}
	identity := record.Resource
	if identity.Backend == "" {
		identity.Backend = record.Backend
	}
	observation, err := controller.Probe(ctx, identity)
	if err != nil {
		s.markUnknown(ctx, record, fmt.Sprintf("probe resource: %v", err))
		return record, pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: err.Error()}, err
	}
	if observation.State == pkgsandbox.ResourceStatePresent {
		observation, err = controller.Terminate(ctx, identity)
		if err != nil {
			s.markUnknown(ctx, record, fmt.Sprintf("terminate resource: %v", err))
			return record, pkgsandbox.ResourceObservation{State: pkgsandbox.ResourceStateUnknown, Detail: err.Error()}, err
		}
	}
	if observation.State == pkgsandbox.ResourceStateAbsent {
		updated, updateErr := s.q.MarkSandboxGenerationReconciledAbsent(ctx, sqlc.MarkSandboxGenerationReconciledAbsentParams{
			SessionID: sessionID, Generation: generation, LastError: observation.Detail,
		})
		if updateErr != nil {
			return record, observation, updateErr
		}
		record = recordFromRow(updated)
		record, err = s.withAuxiliaries(ctx, record)
		if err != nil {
			return record, observation, err
		}
		s.updateEntry(record)
		return record, observation, nil
	}
	s.markUnknown(ctx, record, observation.Detail)
	return record, observation, ErrGenerationUnknown
}

// AcknowledgeAbsent performs the narrow, audited manual declaration used when
// a backend cannot prove absence. force must be true, ownerBootID must match,
// the owner must be drained/stale, and no AgentRun may still be running.
func (s *GenerationStore) AcknowledgeAbsent(ctx context.Context, sessionID string, generation int64, ownerBootID, reason string, force bool) (GenerationRecord, error) {
	if ownerBootID == "" || reason == "" || !force {
		return GenerationRecord{}, ErrGenerationAckDenied
	}
	row, err := s.acknowledgeLocked(ctx, sqlc.AcknowledgeSandboxGenerationAbsentParams{
		SessionID: sessionID, Generation: generation, OwnerBootID: ownerBootID, Reason: reason, Force: force,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return GenerationRecord{}, ErrGenerationAckDenied
	}
	if err != nil {
		return GenerationRecord{}, err
	}
	record := recordFromRow(row)
	s.updateEntry(record)
	s.forgetAuxiliaryEntries(generationKey{sessionID: sessionID, generation: generation})
	return record, nil
}

func (s *GenerationStore) acknowledgeLocked(ctx context.Context, arg sqlc.AcknowledgeSandboxGenerationAbsentParams) (sqlc.AgentSandboxGeneration, error) {
	type beginner interface {
		Begin(context.Context) (pgx.Tx, error)
	}
	db, ok := s.db.(beginner)
	if !ok {
		return sqlc.AgentSandboxGeneration{}, errors.New("sandbox generation acknowledgement requires a transactional database")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return sqlc.AgentSandboxGeneration{}, err
	}
	qtx := s.q.WithTx(tx)
	if err := qtx.LockSandboxGenerationSession(ctx, arg.SessionID); err != nil {
		_ = tx.Rollback(ctx)
		return sqlc.AgentSandboxGeneration{}, err
	}
	if err := qtx.AcknowledgeSandboxGenerationAuxiliaries(ctx, sqlc.AcknowledgeSandboxGenerationAuxiliariesParams{
		SessionID: arg.SessionID, Generation: arg.Generation, LastError: arg.Reason,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return sqlc.AgentSandboxGeneration{}, err
	}
	row, err := qtx.AcknowledgeSandboxGenerationAbsent(ctx, arg)
	if err != nil {
		_ = tx.Rollback(ctx)
		return sqlc.AgentSandboxGeneration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.AgentSandboxGeneration{}, err
	}
	return row, nil
}

// CloseGeneration proves provider absence through the durable controller and
// then marks the owner-created generation destroyed. Callers must perform any
// provider-local bookkeeping only after this method succeeds; raw.Close may
// delete the only addressable object before a controller can probe it.
func (s *GenerationStore) CloseGeneration(ctx context.Context, row GenerationRecord, detail string) error {
	if s == nil || s.q == nil {
		return nil
	}
	if err := s.closeAuxiliaries(ctx, row); err != nil {
		s.markUnknown(ctx, row, fmt.Sprintf("close auxiliary preparation: %v", err))
		return err
	}
	if row.Resource.Authority == "" || row.Resource.Ref == "" {
		s.markUnknown(ctx, row, "resource identity is unavailable; close cannot prove absence")
		return ErrGenerationUnknown
	}
	controller, err := s.controller(ctx, row.Backend)
	if err != nil {
		s.markUnknown(ctx, row, fmt.Sprintf("construct resource controller: %v", err))
		return err
	}
	if controller == nil {
		s.markUnknown(ctx, row, "backend has no durable resource controller; close cannot prove absence")
		return ErrGenerationNoControl
	}
	identity := row.Resource
	if identity.Backend == "" {
		identity.Backend = row.Backend
	}
	observation, err := controller.Probe(ctx, identity)
	if err != nil {
		s.markUnknown(ctx, row, fmt.Sprintf("probe resource after close: %v", err))
		return err
	}
	if observation.State == pkgsandbox.ResourceStatePresent {
		observation, err = controller.Terminate(ctx, identity)
		if err != nil {
			s.markUnknown(ctx, row, fmt.Sprintf("terminate resource after close: %v", err))
			return err
		}
	}
	if observation.State != pkgsandbox.ResourceStateAbsent {
		s.markUnknown(ctx, row, "close did not prove resource absence: "+observation.Detail)
		return ErrGenerationUnknown
	}
	updated, err := s.q.MarkSandboxGenerationDestroyed(ctx, sqlc.MarkSandboxGenerationDestroyedParams{
		SessionID: row.SessionID, Generation: row.Generation, OwnerBootID: s.ownerBootID, LastError: detail,
	})
	if err != nil {
		return err
	}
	s.updateEntry(recordFromRow(updated))
	s.releaseOwner(row.SessionID, row.Generation)
	return nil
}

// markObservedAbsent performs the owner-scoped CAS used by controllerless
// sessions whose raw backend can prove absence after local cleanup. A local
// observation is only valid for the still-fenced/unknown generation held by
// this executor; callers must never use it for a row from another boot.
func (s *GenerationStore) markObservedAbsent(ctx context.Context, row GenerationRecord, detail string) error {
	if err := s.closeAuxiliaries(ctx, row); err != nil {
		s.markUnknown(ctx, row, fmt.Sprintf("close auxiliary preparation: %v", err))
		return err
	}
	updated, err := s.q.MarkSandboxGenerationDestroyed(ctx, sqlc.MarkSandboxGenerationDestroyedParams{
		SessionID: row.SessionID, Generation: row.Generation, OwnerBootID: s.ownerBootID, LastError: detail,
	})
	if err != nil {
		// Another local close/reconcile may have completed the same CAS while
		// this raw was being serialized. Treat an already-destroyed row as the
		// idempotent success it represents, but preserve all other failures for
		// retry and diagnosis.
		if current, getErr := s.q.GetSandboxGeneration(ctx, sqlc.GetSandboxGenerationParams{
			SessionID: row.SessionID, Generation: row.Generation,
		}); getErr == nil && GenerationState(current.State) == GenerationDestroyed {
			s.updateEntry(recordFromRow(current))
			return nil
		}
		return err
	}
	s.updateEntry(recordFromRow(updated))
	s.releaseOwner(row.SessionID, row.Generation)
	return nil
}

// FenceGeneration records that an operation crossed a crash/transport
// boundary. A fenced row cannot be replaced until Reconcile proves absence.
func (s *GenerationStore) FenceGeneration(ctx context.Context, row GenerationRecord, reason string) (GenerationRecord, error) {
	updated, err := s.q.MarkSandboxGenerationFenced(ctx, sqlc.MarkSandboxGenerationFencedParams{
		SessionID: row.SessionID, Generation: row.Generation, LastError: reason,
	})
	if err == nil {
		record := recordFromRow(updated)
		s.updateEntry(record)
		return record, nil
	}
	// An earlier uncertain operation may already have committed the fence. A
	// second owner-close may proceed, while a trigger or transport failure that
	// left the row active remains a hard stop.
	if current, getErr := s.q.GetSandboxGeneration(ctx, sqlc.GetSandboxGenerationParams{SessionID: row.SessionID, Generation: row.Generation}); getErr == nil {
		record := recordFromRow(current)
		if record.State == GenerationFenced || record.State == GenerationUnknown {
			return record, nil
		}
	}
	return row, err
}

func (s *GenerationStore) release(key generationKey) {
	s.mu.Lock()
	if entry := s.entries[key]; entry != nil && entry.refs > 0 {
		entry.refs--
	}
	s.mu.Unlock()
}

func (s *GenerationStore) releaseOwner(sessionID string, generation int64) {
	s.mu.Lock()
	if entry := s.entries[generationKey{sessionID: sessionID, generation: generation}]; entry != nil {
		entry.ownerHeld = false
	}
	s.mu.Unlock()
}

// snapshotOwnerEntries returns the raw sessions still owned by this process.
// The store lock is held only while taking the snapshot. raw.Close takes its
// own close lock and may call back into the store, so closing while holding
// s.mu would create an ABBA deadlock with the raw cleanup path.
func (s *GenerationStore) snapshotOwnerEntries(sessionID string) []*generationRawSession {
	s.mu.Lock()
	entries := make([]*generationRawSession, 0, len(s.entries))
	for key, entry := range s.entries {
		if sessionID != "" && key.sessionID != sessionID {
			continue
		}
		if entry.ownerHeld && entry.raw != nil {
			entries = append(entries, entry.raw)
		}
	}
	s.mu.Unlock()
	return entries
}

// CloseSessionOwner performs the terminal cleanup for one Session after its
// runner has been detached. It closes every owner-held raw generation found in
// the process-local index, then leaves durable state transitions to raw.Close.
// A failed raw close keeps the entry owner-held so a later lifecycle retry can
// make another attempt.
func (s *GenerationStore) CloseSessionOwner(ctx context.Context, sessionID string) error {
	if s == nil || sessionID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var errs []error
	raws := s.snapshotOwnerEntries(sessionID)
	seen := make(map[generationKey]struct{}, len(raws))
	for _, raw := range raws {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		seen[raw.key] = struct{}{}
		if err := s.closeAuxiliaries(ctx, raw.row); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := raw.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, key := range s.snapshotAuxiliaryParents(sessionID) {
		if _, ok := seen[key]; ok {
			continue
		}
		if err := s.closeAuxiliaries(ctx, GenerationRecord{SessionID: key.sessionID, Generation: key.generation}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close performs terminal cleanup for every owner-held raw generation in this
// executor. The snapshot is intentionally local to this store; durable rows
// owned by another boot are reconciled through the admin/controller path.
func (s *GenerationStore) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var errs []error
	raws := s.snapshotOwnerEntries("")
	seen := make(map[generationKey]struct{}, len(raws))
	for _, raw := range raws {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		seen[raw.key] = struct{}{}
		if err := s.closeAuxiliaries(ctx, raw.row); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := raw.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, key := range s.snapshotAuxiliaryParents("") {
		if _, ok := seen[key]; ok {
			continue
		}
		if err := s.closeAuxiliaries(ctx, GenerationRecord{SessionID: key.sessionID, Generation: key.generation}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SandboxConfigDigest hashes only immutable backend/policy/mount inputs. The
// process environment, OAuth tokens and per-turn package selections are
// deliberately excluded so secret rotation never changes generation identity.
func SandboxConfigDigest(backend string, policy pkgsandbox.Policy, mountSources map[string]string) string {
	type digestInput struct {
		Backend      string                      `json:"backend"`
		Filesystem   pkgsandbox.FilesystemPolicy `json:"filesystem"`
		Network      pkgsandbox.NetworkPolicy    `json:"network"`
		InheritEnv   bool                        `json:"inherit_env"`
		MountSources map[string]string           `json:"mount_sources"`
	}
	input := digestInput{
		Backend: backend, Filesystem: policy.Filesystem, Network: policy.Network,
		InheritEnv: policy.InheritEnv, MountSources: mountSources,
	}
	encoded, _ := json.Marshal(input)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
