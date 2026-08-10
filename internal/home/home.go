// Package home owns persistent Home identity, registry state, and Store
// selection. Callers receive opaque sandbox attachments, never host paths.
package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

type Kind string

const (
	PrincipalHome        Kind = "principal"
	AgentHome            Kind = "agent"
	SystemSkillRoot      Kind = "system_skill"
	SystemAgentSkillRoot Kind = "system_agent_skill"
)

const (
	MutableAssetObjectAuthorityMigration = "mutable_asset_object_authority"
	TypedHomeLegacyRegistrationMigration = "typed_home_legacy_registration_v1"
)

func (k Kind) Valid() bool {
	return k == PrincipalHome || k == AgentHome || k == SystemSkillRoot || k == SystemAgentSkillRoot
}

type PrincipalKind string

const (
	UserPrincipal  PrincipalKind = "user"
	GroupPrincipal PrincipalKind = "group"
)

func (k PrincipalKind) Valid() bool { return k == UserPrincipal || k == GroupPrincipal }

type State string

const (
	StateProvisioning State = "provisioning"
	StateReady        State = "ready"
	StateTombstoned   State = "tombstoned"
	StatePurgeFailed  State = "purge_failed"
	StatePurged       State = "purged"
)

func (s State) Valid() bool {
	return s == StateProvisioning || s == StateReady || s == StateTombstoned || s == StatePurgeFailed || s == StatePurged
}

// Key is the only logical identity accepted by this package.
type Key struct {
	Kind          Kind
	PrincipalKind PrincipalKind
	PrincipalID   string
	AgentID       string
}

func Principal(kind PrincipalKind, id string) Key {
	return Key{Kind: PrincipalHome, PrincipalKind: kind, PrincipalID: id}
}

func Agent(kind PrincipalKind, principalID, agentID string) Key {
	return Key{Kind: AgentHome, PrincipalKind: kind, PrincipalID: principalID, AgentID: agentID}
}

func SystemSkills() Key                    { return Key{Kind: SystemSkillRoot} }
func SystemAgentSkills(agentID string) Key { return Key{Kind: SystemAgentSkillRoot, AgentID: agentID} }

func (k Key) Validate() error {
	if !k.Kind.Valid() {
		return fmt.Errorf("home: invalid kind %q", k.Kind)
	}
	validID := func(name, value string) error {
		if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\\`) {
			return fmt.Errorf("home: invalid %s", name)
		}
		return nil
	}
	switch k.Kind {
	case PrincipalHome:
		if !k.PrincipalKind.Valid() || k.AgentID != "" {
			return fmt.Errorf("home: principal requires a principal kind and no agent")
		}
		return validID("principal ID", k.PrincipalID)
	case AgentHome:
		if !k.PrincipalKind.Valid() {
			return fmt.Errorf("home: agent requires a principal kind")
		}
		if err := validID("principal ID", k.PrincipalID); err != nil {
			return err
		}
		return validID("agent ID", k.AgentID)
	case SystemSkillRoot:
		if k.PrincipalKind != "" || k.PrincipalID != "" || k.AgentID != "" {
			return fmt.Errorf("home: system skill root has no owner")
		}
	case SystemAgentSkillRoot:
		if k.PrincipalKind != "" || k.PrincipalID != "" {
			return fmt.Errorf("home: system agent skill root has no principal")
		}
		return validID("agent ID", k.AgentID)
	}
	return nil
}

type Record struct {
	ID      string
	Key     Key
	StoreID string
	Locator string
	State   State
}

// Store owns physical coordinate allocation and operations. Every locator it
// returns is opaque outside this package and must be relative to that Store.
// A missing ready root is storage loss and must never be recreated. Purge must
// be idempotent under crash replay and cancellation-responsive. AcquirePurgeLock
// provides advisory exclusion among cooperating adapter participants that
// preserve their configured lock namespace until Purge returns; provider
// isolation, not this lock, keeps Agents from reaching that namespace.
type Store interface {
	ID() string
	Allocate(Key) (string, error)
	ValidateLocator(Key, string) error
	Ensure(context.Context, Record) error
	InspectReadyRoot(Record) error
	AcquirePurgeLock(context.Context, Record) (func() error, error)
	Purge(context.Context, Record) error
	Attachment(Record, bool) sandbox.HomeAttachment
}

type readyRootPin interface {
	Revalidate() error
	Close() error
}

type readyRootPinner interface {
	PinReadyRoot(Record) (readyRootPin, error)
}

type inspectedReadyRoot struct {
	store Store
	home  Record
}

func (p inspectedReadyRoot) Revalidate() error { return p.store.InspectReadyRoot(p.home) }
func (inspectedReadyRoot) Close() error        { return nil }

type Registry struct {
	db           *pgxpool.Pool
	q            *sqlc.Queries
	stores       map[string]Store
	defaultStore string
	ownerLocks   [ownerLockStripeCount]chan struct{}
	// Shorter intervals are injected only by fault/concurrency tests.
	purgeClaimRenewInterval time.Duration
	purgeClaimRenewTimeout  time.Duration
}

// Phase 1 supports one replica. These bounded process-local stripes serialize
// physical workspace projection with deletion; Phase 3 replaces this ceiling
// with durable SessionSandbox fencing.
const ownerLockStripeCount = 257

func NewRegistry(db *pgxpool.Pool, defaultStore string, stores ...Store) (*Registry, error) {
	if db == nil {
		return nil, errors.New("home: database is required")
	}
	if err := validateStoreID(defaultStore); err != nil {
		return nil, fmt.Errorf("home: invalid default store: %w", err)
	}
	r := &Registry{
		db: db, q: sqlc.New(db), stores: make(map[string]Store), defaultStore: defaultStore,
		purgeClaimRenewInterval: time.Minute, purgeClaimRenewTimeout: 10 * time.Second,
	}
	for i := range r.ownerLocks {
		r.ownerLocks[i] = make(chan struct{}, 1)
		r.ownerLocks[i] <- struct{}{}
	}
	for _, store := range stores {
		if store == nil {
			return nil, errors.New("home: store is required")
		}
		if err := validateStoreID(store.ID()); err != nil {
			return nil, fmt.Errorf("home: invalid store ID: %w", err)
		}
		if _, exists := r.stores[store.ID()]; exists {
			return nil, fmt.Errorf("home: duplicate store %q", store.ID())
		}
		r.stores[store.ID()] = store
	}
	if r.stores[defaultStore] == nil {
		return nil, fmt.Errorf("home: default store %q is not registered", defaultStore)
	}
	return r, nil
}

func (r *Registry) Ensure(ctx context.Context, key Key) (Record, error) {
	return r.ensureWithStore(ctx, r.q, key, r.defaultStore)
}

func (r *Registry) ensureWithStore(ctx context.Context, q *sqlc.Queries, key Key, storeID string) (Record, error) {
	if err := key.Validate(); err != nil {
		return Record{}, err
	}
	row, err := r.getWithQueries(ctx, q, key)
	if errors.Is(err, pgx.ErrNoRows) {
		store := r.stores[storeID]
		if store == nil {
			return Record{}, fmt.Errorf("home: Store %q is not configured", storeID)
		}
		locator, allocateErr := store.Allocate(key)
		if allocateErr != nil {
			return Record{}, fmt.Errorf("home: allocate locator: %w", allocateErr)
		}
		if err := store.ValidateLocator(key, locator); err != nil {
			return Record{}, fmt.Errorf("home: validate allocated locator: %w", err)
		}
		row, err = q.CreateStorageHome(ctx, sqlc.CreateStorageHomeParams{ID: uuid.Must(uuid.NewV7()).String(), HomeKind: string(key.Kind), PrincipalKind: nullable(string(key.PrincipalKind)), PrincipalID: nullable(key.PrincipalID), AgentID: nullable(key.AgentID), StoreID: store.ID(), Locator: locator})
		if errors.Is(err, pgx.ErrNoRows) {
			row, err = r.getWithQueries(ctx, q, key)
		}
	}
	if err != nil {
		return Record{}, fmt.Errorf("home: ensure registry row: %w", err)
	}
	home, err := r.decode(row)
	if err != nil {
		return Record{}, err
	}
	if home.State == StateTombstoned || home.State == StatePurgeFailed || home.State == StatePurged {
		return Record{}, fmt.Errorf("home: %s Home cannot be ensured or attached", home.State)
	}
	if home.State == StateProvisioning {
		if err := r.stores[home.StoreID].Ensure(ctx, home); err != nil {
			return Record{}, fmt.Errorf("home: ensure physical storage: %w", err)
		}
		row, err = q.MarkStorageHomeReady(ctx, home.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			row, err = q.GetStorageHome(ctx, home.ID)
		}
		if err != nil {
			return Record{}, fmt.Errorf("home: mark ready: %w", err)
		}
		ready, err := r.decode(row)
		if err != nil {
			return Record{}, err
		}
		if ready.State != StateReady {
			return Record{}, fmt.Errorf("home: ready transition lost to %s", ready.State)
		}
		return ready, nil
	}
	if err := r.stores[home.StoreID].InspectReadyRoot(home); err != nil {
		return Record{}, fmt.Errorf("home: ready storage is unavailable: %w", err)
	}
	return home, nil
}

func (r *Registry) Resolve(ctx context.Context, key Key, readOnly bool) (sandbox.HomeAttachment, error) {
	return r.resolveWithQueries(ctx, r.q, key, readOnly)
}

func (r *Registry) resolveWithQueries(ctx context.Context, q *sqlc.Queries, key Key, readOnly bool) (sandbox.HomeAttachment, error) {
	if err := key.Validate(); err != nil {
		return sandbox.HomeAttachment{}, err
	}
	row, err := r.getWithQueries(ctx, q, key)
	if err != nil {
		return sandbox.HomeAttachment{}, fmt.Errorf("home: resolve registry row: %w", err)
	}
	home, err := r.decode(row)
	if err != nil {
		return sandbox.HomeAttachment{}, err
	}
	pin, err := r.pinReadyRoot(home)
	if err != nil {
		return sandbox.HomeAttachment{}, fmt.Errorf("home: ready storage is unavailable: %w", err)
	}
	defer pin.Close() //nolint:errcheck
	attachment, err := r.resolveRecordWithQueries(ctx, q, home, readOnly)
	if err != nil {
		return sandbox.HomeAttachment{}, err
	}
	if err := pin.Revalidate(); err != nil {
		return sandbox.HomeAttachment{}, fmt.Errorf("home: ready storage changed during revalidation: %w", err)
	}
	return attachment, nil
}

// resolveRecord re-reads the row by immutable ID before issuing an attachment.
// This closes the cutover/tombstone window between Ensure and compatibility
// projection; a stale, forged, or non-ready record never obtains a local path.
func (r *Registry) resolveRecord(ctx context.Context, record Record, readOnly bool) (sandbox.HomeAttachment, error) {
	store := r.stores[record.StoreID]
	if store == nil {
		return sandbox.HomeAttachment{}, fmt.Errorf("home: Store %q is not configured", record.StoreID)
	}
	pin, err := r.pinReadyRoot(record)
	if err != nil {
		return sandbox.HomeAttachment{}, fmt.Errorf("home: ready storage is unavailable: %w", err)
	}
	defer pin.Close() //nolint:errcheck
	attachment, err := r.resolveRecordWithQueries(ctx, r.q, record, readOnly)
	if err != nil {
		return sandbox.HomeAttachment{}, err
	}
	if err := pin.Revalidate(); err != nil {
		return sandbox.HomeAttachment{}, fmt.Errorf("home: ready storage changed during revalidation: %w", err)
	}
	return attachment, nil
}

func (r *Registry) pinReadyRoot(record Record) (readyRootPin, error) {
	store := r.stores[record.StoreID]
	if store == nil {
		return nil, fmt.Errorf("home: Store %q is not configured", record.StoreID)
	}
	if pinner, ok := store.(readyRootPinner); ok {
		return pinner.PinReadyRoot(record)
	}
	if err := store.InspectReadyRoot(record); err != nil {
		return nil, err
	}
	return inspectedReadyRoot{store: store, home: record}, nil
}

func (r *Registry) resolveRecordWithQueries(ctx context.Context, q *sqlc.Queries, record Record, readOnly bool) (sandbox.HomeAttachment, error) {
	row, err := q.GetStorageHome(ctx, record.ID)
	if err != nil {
		return sandbox.HomeAttachment{}, fmt.Errorf("home: revalidate attachment: %w", err)
	}
	home, err := r.decode(row)
	if err != nil {
		return sandbox.HomeAttachment{}, err
	}
	if home.Key != record.Key || home.StoreID != record.StoreID || home.Locator != record.Locator || home.State != StateReady {
		return sandbox.HomeAttachment{}, errors.New("home: stale or unavailable attachment")
	}
	if home.Key.Kind == SystemSkillRoot || home.Key.Kind == SystemAgentSkillRoot {
		readOnly = true
	}
	return r.stores[home.StoreID].Attachment(home, readOnly), nil
}

func (r *Registry) Tombstone(ctx context.Context, key Key, actor string) (Record, error) {
	if strings.TrimSpace(actor) == "" {
		return Record{}, errors.New("home: tombstone actor is required")
	}
	if err := key.Validate(); err != nil {
		return Record{}, err
	}
	row, err := r.get(ctx, key)
	if err != nil {
		return Record{}, fmt.Errorf("home: find for tombstone: %w", err)
	}
	home, err := r.decode(row)
	if err != nil {
		return Record{}, err
	}
	if home.State == StateTombstoned || home.State == StatePurgeFailed || home.State == StatePurged {
		return home, nil
	}
	row, err = r.q.TombstoneStorageHome(ctx, sqlc.TombstoneStorageHomeParams{ID: home.ID, TombstonedBy: nullable(actor)})
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = r.q.GetStorageHome(ctx, home.ID)
	}
	if err != nil {
		return Record{}, fmt.Errorf("home: tombstone: %w", err)
	}
	return r.decode(row)
}

// PhysicalPurgeError proves that the physical Store operation failed and that
// the durable registry row was parked in purge_failed. Workers may suppress
// only this error; all other failures are unsafe to acknowledge.
type PhysicalPurgeError struct{ err error }

func (e *PhysicalPurgeError) Error() string { return fmt.Sprintf("home: physical purge: %v", e.err) }
func (e *PhysicalPurgeError) Unwrap() error { return e.err }

var (
	ErrPurgeInProgress = errors.New("home: physical purge is already in progress")
	ErrPurgeClaimLost  = errors.New("home: physical purge claim was lost")
)

// Purge performs the physical half of a previously fenced deletion. It is
// idempotent for purged records and records a physical failure durably.
func (r *Registry) Purge(ctx context.Context, id, actor string) (Record, error) {
	return r.purge(ctx, id, actor, false)
}

func (r *Registry) purge(ctx context.Context, id, actor string, requireFailed bool) (Record, error) {
	if strings.TrimSpace(actor) == "" {
		return Record{}, errors.New("home: purge actor is required")
	}
	claimToken := uuid.Must(uuid.NewV7()).String()
	expectedState := StateTombstoned
	if requireFailed {
		expectedState = StatePurgeFailed
	}
	var row sqlc.StorageHome
	for {
		var err error
		row, err = r.q.ClaimStorageHomePurge(ctx, sqlc.ClaimStorageHomePurgeParams{
			ID: id, PurgeClaimToken: text(claimToken), ExpectedState: string(expectedState),
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			if err != nil {
				return Record{}, fmt.Errorf("home: begin purge: %w", err)
			}
			break
		}
		status, readErr := r.q.GetStorageHomePurgeClaimStatus(ctx, id)
		if readErr != nil {
			return Record{}, fmt.Errorf("home: classify purge claim: %w", readErr)
		}
		if status.PurgeClaimActive.Valid && status.PurgeClaimActive.Bool {
			return Record{}, ErrPurgeInProgress
		}
		if State(status.State) == expectedState {
			continue // The database clock says a racing claim is now reclaimable.
		}
		if State(status.State) == StatePurged && !requireFailed {
			current, getErr := r.q.GetStorageHome(ctx, id)
			if getErr != nil {
				return Record{}, fmt.Errorf("home: reload purged Home: %w", getErr)
			}
			return r.decode(current)
		}
		if State(status.State) == StatePurgeFailed && !requireFailed {
			current, getErr := r.q.GetStorageHome(ctx, id)
			if getErr != nil {
				return Record{}, fmt.Errorf("home: reload failed purge: %w", getErr)
			}
			return r.decode(current)
		}
		return Record{}, fmt.Errorf("home: %s cannot claim purge", status.State)
	}
	home, err := r.decode(row)
	if err != nil {
		return Record{}, err
	}
	operationCtx, stopRenewal := r.renewPurgeClaim(ctx, id, claimToken)
	release, err := r.stores[home.StoreID].AcquirePurgeLock(operationCtx, home)
	if err != nil {
		renewErr := stopRenewal()
		if renewErr != nil {
			return Record{}, renewErr
		}
		return Record{}, fmt.Errorf("home: acquire physical purge lock: %w", err)
	}
	defer func() {
		_ = stopRenewal()
		_ = release()
	}()
	claimCurrent, err := r.q.ValidateStorageHomePurgeClaim(operationCtx, sqlc.ValidateStorageHomePurgeClaimParams{ID: id, PurgeClaimToken: text(claimToken)})
	if err != nil {
		return Record{}, fmt.Errorf("home: revalidate physical purge claim: %w", err)
	}
	if !claimCurrent {
		return Record{}, ErrPurgeClaimLost
	}
	physicalErr := r.stores[home.StoreID].Purge(operationCtx, home)
	if cause := context.Cause(operationCtx); cause != nil {
		return Record{}, cause
	}
	if renewErr := stopRenewal(); renewErr != nil {
		return Record{}, renewErr
	}
	if physicalErr != nil {
		_, markErr := r.q.MarkStorageHomePurgeFailed(operationCtx, sqlc.MarkStorageHomePurgeFailedParams{ID: id, LastPurgeError: nullable(physicalErr.Error()), PurgeClaimToken: text(claimToken)})
		if markErr != nil {
			return Record{}, fmt.Errorf("home: physical purge: %w; record failure: %w", physicalErr, markErr)
		}
		return Record{}, &PhysicalPurgeError{err: physicalErr}
	}
	row, err = r.q.MarkStorageHomePurged(operationCtx, sqlc.MarkStorageHomePurgedParams{ID: id, PurgedBy: nullable(actor), PurgeClaimToken: text(claimToken)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrPurgeClaimLost
	}
	if err != nil {
		return Record{}, fmt.Errorf("home: mark purged: %w", err)
	}
	return r.decode(row)
}

func (r *Registry) renewPurgeClaim(ctx context.Context, id, token string) (context.Context, func() error) {
	operationCtx, cancelOperation := context.WithCancelCause(ctx)
	stop := make(chan struct{})
	done := make(chan struct{})
	var renewalErr error
	go func() {
		defer close(done)
		ticker := time.NewTicker(r.purgeClaimRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-operationCtx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(operationCtx, r.purgeClaimRenewTimeout)
				rows, err := r.q.RenewStorageHomePurgeClaim(renewCtx, sqlc.RenewStorageHomePurgeClaimParams{ID: id, PurgeClaimToken: text(token)})
				renewCancel()
				if err != nil {
					renewalErr = fmt.Errorf("home: renew physical purge claim: %w", err)
					cancelOperation(renewalErr)
					return
				}
				if rows != 1 {
					renewalErr = ErrPurgeClaimLost
					cancelOperation(renewalErr)
					return
				}
			}
		}
	}()
	var stopOnce sync.Once
	return operationCtx, func() error {
		stopOnce.Do(func() { close(stop) })
		<-done
		return renewalErr
	}
}

// Record reads one registry record for lifecycle workers without exposing raw
// sqlc state to callers.
func (r *Registry) Record(ctx context.Context, id string) (Record, error) {
	row, err := r.q.GetStorageHome(ctx, id)
	if err != nil {
		return Record{}, fmt.Errorf("home: get record: %w", err)
	}
	return r.decode(row)
}

// RetryFailedPurge retries exactly one durably parked physical failure. It
// refuses tombstoned, ready, purged, and missing records, so maintenance cannot
// turn an ordinary pending deletion into an immediate purge.
func (r *Registry) RetryFailedPurge(ctx context.Context, id, actor string) (Record, error) {
	return r.purge(ctx, id, actor, true)
}

type MaintenanceLease struct {
	Home  Record
	Owner string
	Token string
	Until time.Time
}

func (r *Registry) AcquireMaintenance(ctx context.Context, id, owner string, until time.Time) (MaintenanceLease, error) {
	if strings.TrimSpace(owner) == "" {
		return MaintenanceLease{}, errors.New("home: maintenance owner is required")
	}
	if until.IsZero() || !until.After(time.Now().UTC()) {
		return MaintenanceLease{}, errors.New("home: maintenance lease must be in the future")
	}
	token := uuid.Must(uuid.NewV7()).String()
	row, err := r.q.AcquireStorageHomeMaintenance(ctx, sqlc.AcquireStorageHomeMaintenanceParams{ID: id, MaintenanceOwner: nullable(owner), MaintenanceToken: text(token), MaintenanceUntil: pgtype.Timestamptz{Time: until.UTC(), Valid: true}})
	if err != nil {
		return MaintenanceLease{}, fmt.Errorf("home: acquire maintenance: %w", err)
	}
	home, err := r.decode(row)
	if err != nil {
		return MaintenanceLease{}, err
	}
	return MaintenanceLease{Home: home, Owner: owner, Token: token, Until: until.UTC()}, nil
}

func (r *Registry) ReleaseMaintenance(ctx context.Context, id, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("home: maintenance token is required")
	}
	rows, err := r.q.ReleaseStorageHomeMaintenance(ctx, sqlc.ReleaseStorageHomeMaintenanceParams{ID: id, MaintenanceToken: text(token)})
	if err != nil {
		return fmt.Errorf("home: release maintenance: %w", err)
	}
	if rows != 1 {
		return errors.New("home: maintenance lease is stale")
	}
	return nil
}

// CutoverStore records a completed offline copy. The caller is responsible for
// fencing consumers and verifying physical data before this CAS; no filesystem
// work occurs in the database transaction.
func (r *Registry) CutoverStore(ctx context.Context, current Record, newStoreID, newLocator, token string) (Record, error) {
	if err := validateStoreID(newStoreID); err != nil {
		return Record{}, fmt.Errorf("home: invalid target store: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return Record{}, errors.New("home: maintenance token is required")
	}
	row, err := r.q.GetStorageHome(ctx, current.ID)
	if err != nil {
		return Record{}, fmt.Errorf("home: find for store cutover: %w", err)
	}
	persisted, err := r.decode(row)
	if err != nil {
		return Record{}, err
	}
	if current.StoreID != persisted.StoreID || current.Locator != persisted.Locator {
		return Record{}, errors.New("home: stale store cutover record")
	}
	if persisted.Key.Kind == SystemSkillRoot || persisted.Key.Kind == SystemAgentSkillRoot {
		return Record{}, errors.New("home: shared Skill root Store cutover requires Phase 2 Skill consumers and mounts to derive coordinates from attachments")
	}
	store := r.stores[newStoreID]
	if store == nil {
		return Record{}, fmt.Errorf("home: target store %q is not configured", newStoreID)
	}
	if err := store.ValidateLocator(persisted.Key, newLocator); err != nil {
		return Record{}, fmt.Errorf("home: invalid target locator: %w", err)
	}
	row, err = r.q.CutoverStorageHomeStore(ctx, sqlc.CutoverStorageHomeStoreParams{ID: persisted.ID, StoreID: persisted.StoreID, Locator: persisted.Locator, StoreID_2: newStoreID, Locator_2: newLocator, MaintenanceToken: text(token)})
	if err != nil {
		return Record{}, fmt.Errorf("home: cut over store: %w", err)
	}
	return r.decode(row)
}

// lockOwnerKeys serializes only local filesystem projection and owner deletion.
// It deliberately uses ownerLockKey vocabulary and bounded stripes rather than
// retaining a mutex per owner forever.
func (r *Registry) lockOwnerKeys(ctx context.Context, keys []string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("home: acquire owner lifecycle lock: %w", err)
	}
	stripes := make([]int, 0, len(keys))
	for _, key := range keys {
		stripes = append(stripes, ownerLockStripe(key))
	}
	sort.Ints(stripes)
	unique := stripes[:0]
	for _, stripe := range stripes {
		if len(unique) == 0 || unique[len(unique)-1] != stripe {
			unique = append(unique, stripe)
		}
	}
	acquired := make([]int, 0, len(unique))
	release := func() {
		for i := len(acquired) - 1; i >= 0; i-- {
			r.ownerLocks[acquired[i]] <- struct{}{}
		}
	}
	for _, stripe := range unique {
		select {
		case <-ctx.Done():
			release()
			return nil, fmt.Errorf("home: acquire owner lifecycle lock: %w", ctx.Err())
		case <-r.ownerLocks[stripe]:
			acquired = append(acquired, stripe)
		}
	}
	return release, nil
}

func ownerLockStripe(key string) int {
	// FNV-1a is stable across processes, useful when diagnosing a blocked stripe.
	hash := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		hash ^= uint32(key[i])
		hash *= 16777619
	}
	return int(hash % ownerLockStripeCount)
}

// ObserveMutableAssetObjectAuthority records only whether legacy mutable asset
// object storage was configured. It deliberately stores no endpoint or secret
// and changes no current asset mirror/hydrate behavior.
func (r *Registry) ObserveMutableAssetObjectAuthority(ctx context.Context, configured bool) error {
	state := "not_required"
	if configured {
		state = "pending"
	}
	marker, err := r.q.UpsertStorageMigrationObservation(ctx, sqlc.UpsertStorageMigrationObservationParams{Name: MutableAssetObjectAuthorityMigration, State: state, ObjectAuthorityConfigured: configured, Metadata: json.RawMessage(`{}`)})
	if errors.Is(err, pgx.ErrNoRows) {
		marker, err = r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	}
	if err != nil {
		return fmt.Errorf("home: record mutable asset authority: %w", err)
	}
	if marker.State == "completed" && !marker.ObjectAuthorityConfigured && configured {
		return errors.New("home: completed mutable asset authority migration conflicts with configured object authority")
	}
	return nil
}

// ValidateConfiguredStores fails startup before a dormant Home reaches a later
// consumer with a Store unavailable in this deployment.
func (r *Registry) ValidateConfiguredStores(ctx context.Context) error {
	ids, err := r.q.ListStorageHomeStoreID(ctx)
	if err != nil {
		return fmt.Errorf("home: list referenced stores: %w", err)
	}
	for _, id := range ids {
		if err := validateStoreID(id); err != nil || r.stores[id] == nil {
			return fmt.Errorf("home: referenced store %q is not configured", id)
		}
	}
	return nil
}

type legacyAgentLister interface {
	LegacyAgentIDs(Key) ([]string, error)
}

// RegisterLegacy registers only identities already known to PostgreSQL. Disk
// inspection is limited to child Agent directories under those known roots.
func (r *Registry) RegisterLegacy(ctx context.Context) error {
	if err := r.ValidateConfiguredStores(ctx); err != nil {
		return err
	}
	marker, legacyStore, err := r.legacyRegistrationMarker(ctx)
	if err != nil {
		return err
	}
	if marker.State == "completed" {
		return nil
	}
	if marker.State != "pending" {
		return fmt.Errorf("home: legacy registration has invalid state %q", marker.State)
	}
	lister, ok := legacyStore.(legacyAgentLister)
	if !ok {
		return fmt.Errorf("home: legacy Store %q cannot inspect legacy AgentHomes", legacyStore.ID())
	}
	users, err := r.q.ListStorageLegacyUserID(ctx)
	if err != nil {
		return fmt.Errorf("home: list legacy users: %w", err)
	}
	groups, err := r.q.ListStorageLegacyGroupID(ctx)
	if err != nil {
		return fmt.Errorf("home: list legacy groups: %w", err)
	}
	agents, err := r.q.ListStorageLegacyAgentID(ctx)
	if err != nil {
		return fmt.Errorf("home: list legacy agents: %w", err)
	}
	knownAgents := make(map[string]struct{}, len(agents))
	for _, id := range agents {
		knownAgents[id] = struct{}{}
		if _, err := r.ensureWithStore(ctx, r.q, SystemAgentSkills(id), legacyStore.ID()); err != nil {
			return fmt.Errorf("home: register system Agent Skill root %q: %w", id, err)
		}
	}
	if _, err := r.ensureWithStore(ctx, r.q, SystemSkills(), legacyStore.ID()); err != nil {
		return fmt.Errorf("home: register system Skill root: %w", err)
	}
	principals := make([]Key, 0, len(users)+len(groups))
	for _, id := range users {
		principals = append(principals, Principal(UserPrincipal, id))
	}
	for _, id := range groups {
		principals = append(principals, Principal(GroupPrincipal, id))
	}
	for _, key := range principals {
		if _, err := r.ensureWithStore(ctx, r.q, key, legacyStore.ID()); err != nil {
			return fmt.Errorf("home: register principal: %w", err)
		}
	}
	userAgents, err := r.q.ListStorageLegacyUserAgent(ctx)
	if err != nil {
		return fmt.Errorf("home: list legacy user assignments: %w", err)
	}
	for _, assignment := range userAgents {
		if _, ok := knownAgents[assignment.AgentID]; !ok {
			return fmt.Errorf("home: user assignment references unknown Agent %q", assignment.AgentID)
		}
		if _, err := r.ensureWithStore(ctx, r.q, Agent(UserPrincipal, assignment.UserID, assignment.AgentID), legacyStore.ID()); err != nil {
			return fmt.Errorf("home: register user AgentHome: %w", err)
		}
	}
	groupAgents, err := r.q.ListStorageLegacyGroupAgent(ctx)
	if err != nil {
		return fmt.Errorf("home: list legacy group assignments: %w", err)
	}
	for _, assignment := range groupAgents {
		if _, ok := knownAgents[assignment.AgentID]; !ok {
			return fmt.Errorf("home: group assignment references unknown Agent %q", assignment.AgentID)
		}
		if _, err := r.ensureWithStore(ctx, r.q, Agent(GroupPrincipal, assignment.GroupID, assignment.AgentID), legacyStore.ID()); err != nil {
			return fmt.Errorf("home: register group AgentHome: %w", err)
		}
	}
	for _, principal := range principals {
		ids, err := lister.LegacyAgentIDs(principal)
		if err != nil {
			return fmt.Errorf("home: inspect legacy AgentHomes: %w", err)
		}
		for _, id := range ids {
			if _, ok := knownAgents[id]; !ok {
				continue
			}
			if _, err := r.ensureWithStore(ctx, r.q, Agent(principal.PrincipalKind, principal.PrincipalID, id), legacyStore.ID()); err != nil {
				return fmt.Errorf("home: register legacy AgentHome: %w", err)
			}
		}
	}
	if _, err := r.q.CompleteStorageMigration(ctx, sqlc.CompleteStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, State: "pending"}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("home: complete legacy registration: %w", err)
		}
		marker, getErr := r.q.GetStorageMigration(ctx, TypedHomeLegacyRegistrationMigration)
		if getErr != nil {
			return fmt.Errorf("home: reload legacy registration completion: %w", getErr)
		}
		if marker.State != "completed" {
			return fmt.Errorf("home: legacy registration completion CAS lost to state %q", marker.State)
		}
	}
	return nil
}

func (r *Registry) legacyRegistrationMarker(ctx context.Context) (sqlc.StorageMigration, Store, error) {
	marker, err := r.q.GetStorageMigration(ctx, TypedHomeLegacyRegistrationMigration)
	if err == nil {
		store, validateErr := r.legacyRegistrationStore(marker)
		return marker, store, validateErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.StorageMigration{}, nil, fmt.Errorf("home: load legacy registration marker: %w", err)
	}
	metadata, marshalErr := json.Marshal(map[string]string{"store_id": r.defaultStore})
	if marshalErr != nil {
		return sqlc.StorageMigration{}, nil, fmt.Errorf("home: encode legacy registration metadata: %w", marshalErr)
	}
	marker, err = r.q.CreateStorageMigration(ctx, sqlc.CreateStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, Metadata: metadata})
	if !errors.Is(err, pgx.ErrNoRows) {
		if err != nil {
			return sqlc.StorageMigration{}, nil, fmt.Errorf("home: create legacy registration marker: %w", err)
		}
		store, validateErr := r.legacyRegistrationStore(marker)
		return marker, store, validateErr
	}
	marker, err = r.q.GetStorageMigration(ctx, TypedHomeLegacyRegistrationMigration)
	if err != nil {
		return sqlc.StorageMigration{}, nil, fmt.Errorf("home: reload legacy registration marker: %w", err)
	}
	store, validateErr := r.legacyRegistrationStore(marker)
	return marker, store, validateErr
}

func (r *Registry) legacyRegistrationStore(marker sqlc.StorageMigration) (Store, error) {
	var metadata struct {
		StoreID string `json:"store_id"`
	}
	if err := json.Unmarshal(marker.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("home: decode legacy registration metadata: %w", err)
	}
	if err := validateStoreID(metadata.StoreID); err != nil {
		return nil, fmt.Errorf("home: legacy registration metadata has invalid Store ID: %w", err)
	}
	// A completed marker is an audit record, not a live attachment. Its Store
	// identity must remain well-formed, but retiring that adapter is safe once
	// no further legacy inventory can run.
	if marker.State == "completed" {
		return nil, nil
	}
	store := r.stores[metadata.StoreID]
	if store == nil {
		return nil, fmt.Errorf("home: legacy registration Store %q is not configured", metadata.StoreID)
	}
	return store, nil
}

func (r *Registry) get(ctx context.Context, key Key) (sqlc.StorageHome, error) {
	return r.getWithQueries(ctx, r.q, key)
}

func (r *Registry) getWithQueries(ctx context.Context, q *sqlc.Queries, key Key) (sqlc.StorageHome, error) {
	switch key.Kind {
	case PrincipalHome:
		return q.GetPrincipalStorageHome(ctx, sqlc.GetPrincipalStorageHomeParams{PrincipalKind: nullable(string(key.PrincipalKind)), PrincipalID: nullable(key.PrincipalID)})
	case AgentHome:
		return q.GetAgentStorageHome(ctx, sqlc.GetAgentStorageHomeParams{PrincipalKind: nullable(string(key.PrincipalKind)), PrincipalID: nullable(key.PrincipalID), AgentID: nullable(key.AgentID)})
	case SystemSkillRoot:
		return q.GetSystemSkillStorageHome(ctx)
	case SystemAgentSkillRoot:
		return q.GetSystemAgentSkillStorageHome(ctx, nullable(key.AgentID))
	}
	return sqlc.StorageHome{}, fmt.Errorf("home: unsupported kind %q", key.Kind)
}

func (r *Registry) decode(row sqlc.StorageHome) (Record, error) {
	home := Record{ID: row.ID, Key: Key{Kind: Kind(row.HomeKind), PrincipalKind: PrincipalKind(row.PrincipalKind.String), PrincipalID: row.PrincipalID.String, AgentID: row.AgentID.String}, StoreID: row.StoreID, Locator: row.Locator, State: State(row.State)}
	if home.ID == "" || home.Key.Validate() != nil || !home.State.Valid() {
		return Record{}, errors.New("home: corrupt registry identity or lifecycle")
	}
	if err := validateStoreID(home.StoreID); err != nil {
		return Record{}, fmt.Errorf("home: corrupt store ID: %w", err)
	}
	store := r.stores[home.StoreID]
	if store == nil {
		return Record{}, fmt.Errorf("home: referenced store %q is not configured", home.StoreID)
	}
	if err := store.ValidateLocator(home.Key, home.Locator); err != nil {
		return Record{}, fmt.Errorf("home: corrupt locator: %w", err)
	}
	return home, nil
}

func validateStoreID(id string) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id {
		return errors.New("must be a non-empty logical identifier")
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return errors.New("must be a safe logical identifier")
		}
	}
	return nil
}

func nullable(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
