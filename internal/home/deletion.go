package home

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// OwnerKind names the durable owner whose Homes are permanently deleted.
type OwnerKind string

const (
	OwnerUser  OwnerKind = "user"
	OwnerGroup OwnerKind = "group"
	OwnerAgent OwnerKind = "agent"
	// Physical failures return nil only after their purge_failed audit commits;
	// everything else is retried by River within this finite safety budget.
	ownerPurgeMaxAttempts = 25
)

// HomePurgeQueue serializes durable physical Home purges.
const HomePurgeQueue = "home_purge"

// OwnerFencer synchronously makes an owner's cached execution state terminal.
// It is deliberately narrower than PoolManager's ordinary invalidation surface.
type OwnerFencer interface {
	FenceHomeOwner(context.Context, OwnerKind, string) error
}

type ownerDeleteEnqueuer interface {
	InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// OwnerDeletion is the deep lifecycle boundary for destructive owner deletes.
// It owns the transaction that makes Homes unavailable, removes the owner, and
// durably schedules exactly one physical-purge batch.
type OwnerDeletion struct {
	db       *pgxpool.Pool
	registry *Registry
	enqueue  ownerDeleteEnqueuer
	fencer   OwnerFencer
}

func NewOwnerDeletion(db *pgxpool.Pool, registry *Registry, enqueue ownerDeleteEnqueuer, fencer OwnerFencer) (*OwnerDeletion, error) {
	if db == nil || registry == nil || fencer == nil {
		return nil, errors.New("home: owner deletion requires database, registry, and fencer")
	}
	return &OwnerDeletion{db: db, registry: registry, enqueue: enqueue, fencer: fencer}, nil
}

// BindRiverClient is the one pre-start bind from the shared River composition
// root. Tests may instead construct OwnerDeletion with a narrow fake enqueuer.
func (d *OwnerDeletion) BindRiverClient(client ownerDeleteEnqueuer) error {
	if client == nil {
		return errors.New("home: bind owner deletion River client: nil client")
	}
	if d.enqueue != nil {
		return errors.New("home: owner deletion River client already bound")
	}
	d.enqueue = client
	return nil
}

// DeleteGroup atomically tombstones all group Homes, deletes the group, and
// enqueues one purge batch. The caller authorizes before reaching this boundary.
func (d *OwnerDeletion) DeleteGroup(ctx context.Context, id, actor string) error {
	return d.delete(ctx, OwnerGroup, id, actor, func(ctx context.Context, q *sqlc.Queries, id string) error {
		return q.DeleteGroupState(ctx, id)
	})
}

// DeleteAgent preserves Agent provider credentials by relying on the existing
// agent-row FK semantics; an in-use delete rolls every Home transition back.
func (d *OwnerDeletion) DeleteAgent(ctx context.Context, id, actor string) error {
	return d.delete(ctx, OwnerAgent, id, actor, func(ctx context.Context, q *sqlc.Queries, id string) error {
		return q.DeleteAgent(ctx, id)
	})
}

// DeleteUser is deliberately internal until a product account-delete flow exists.
func (d *OwnerDeletion) DeleteUser(ctx context.Context, id, actor string) error {
	return d.delete(ctx, OwnerUser, id, actor, func(ctx context.Context, q *sqlc.Queries, id string) error {
		return q.DeleteAuthUser(ctx, id)
	})
}

type ownerDeleteFunc func(context.Context, *sqlc.Queries, string) error

func (d *OwnerDeletion) delete(ctx context.Context, kind OwnerKind, id, actor string, deleteOwner ownerDeleteFunc) error {
	if d == nil || d.db == nil || d.registry == nil || d.enqueue == nil || d.fencer == nil {
		return errors.New("home: owner deletion lifecycle is unavailable")
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(actor) == "" {
		return errors.New("home: owner deletion ID and actor are required")
	}
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("home: begin owner deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	if err := q.LockStorageHomeOwner(ctx, ownerLockKey(kind, id)); err != nil {
		return fmt.Errorf("home: lock %s Home lifecycle: %w", kind, err)
	}

	// Lock/verify first: no tombstone may survive a failed owner delete.
	if err := d.lockOwner(ctx, q, kind, id); err != nil {
		return err
	}
	rows, err := d.tombstoneHomes(ctx, q, kind, id, actor)
	if err != nil {
		return err
	}
	if err := deleteOwner(ctx, q, id); err != nil {
		return err
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	if _, err := d.enqueue.InsertTx(ctx, tx, ownerPurgeArgs{OwnerKind: kind, OwnerID: id, HomeIDs: ids, Actor: actor}, ownerPurgeInsertOpts()); err != nil {
		return fmt.Errorf("home: enqueue owner purge: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("home: commit owner deletion: %w", err)
	}
	// The durable worker fences again before physical purge. Fence now too: a
	// successful delete response must not leave a cached runner discoverable.
	if err := d.fencer.FenceHomeOwner(ctx, kind, id); err != nil {
		return fmt.Errorf("home: owner deletion committed but execution fence is unavailable; River will retry fencing before purge: %w", err)
	}
	return nil
}

func ownerLockKey(kind OwnerKind, id string) string { return string(kind) + ":" + id }

func (d *OwnerDeletion) lockOwner(ctx context.Context, q *sqlc.Queries, kind OwnerKind, id string) error {
	var err error
	switch kind {
	case OwnerGroup:
		_, err = q.GetGroupStateByIDForUpdate(ctx, id)
	case OwnerAgent:
		_, err = q.GetAgentForUpdate(ctx, id)
	case OwnerUser:
		_, err = q.GetAuthUserForUpdate(ctx, id)
	default:
		return fmt.Errorf("home: unsupported deletion owner %q", kind)
	}
	if err != nil {
		return fmt.Errorf("home: lock %s owner: %w", kind, err)
	}
	return nil
}

func (d *OwnerDeletion) tombstoneHomes(ctx context.Context, q *sqlc.Queries, kind OwnerKind, id, actor string) ([]sqlc.StorageHome, error) {
	var sentinel Key
	var rows []sqlc.StorageHome
	var err error
	switch kind {
	case OwnerUser:
		sentinel = Principal(UserPrincipal, id)
	case OwnerGroup:
		sentinel = Principal(GroupPrincipal, id)
	case OwnerAgent:
		sentinel = SystemAgentSkills(id)
	}
	if _, err := d.ensureTx(ctx, q, sentinel); err != nil {
		return nil, err
	}
	switch kind {
	case OwnerUser:
		rows, err = q.ListStorageHomeByPrincipalForUpdate(ctx, sqlc.ListStorageHomeByPrincipalForUpdateParams{PrincipalKind: text(string(UserPrincipal)), PrincipalID: text(id)})
	case OwnerGroup:
		rows, err = q.ListStorageHomeByPrincipalForUpdate(ctx, sqlc.ListStorageHomeByPrincipalForUpdateParams{PrincipalKind: text(string(GroupPrincipal)), PrincipalID: text(id)})
	case OwnerAgent:
		rows, err = q.ListStorageHomeByAgentForUpdate(ctx, text(id))
	}
	if err != nil {
		return nil, fmt.Errorf("home: list owner Homes: %w", err)
	}
	out := make([]sqlc.StorageHome, 0, len(rows))
	for _, row := range rows {
		if State(row.State) == StateTombstoned || State(row.State) == StatePurgeFailed || State(row.State) == StatePurged {
			return nil, fmt.Errorf("home: owner has already deleted Home %s", row.ID)
		}
		tombstoned, err := q.TombstoneStorageHome(ctx, sqlc.TombstoneStorageHomeParams{ID: row.ID, TombstonedBy: text(actor)})
		if err != nil {
			return nil, fmt.Errorf("home: tombstone %s: %w", row.ID, err)
		}
		out = append(out, tombstoned)
	}
	return out, nil
}

// ensureTx records an immutable sentinel but never materializes its Store.
func (d *OwnerDeletion) ensureTx(ctx context.Context, q *sqlc.Queries, key Key) (sqlc.StorageHome, error) {
	if err := key.Validate(); err != nil {
		return sqlc.StorageHome{}, err
	}
	store := d.registry.stores[d.registry.defaultStore]
	locator, err := store.Allocate(key)
	if err != nil {
		return sqlc.StorageHome{}, fmt.Errorf("home: allocate deletion sentinel: %w", err)
	}
	if err := store.ValidateLocator(key, locator); err != nil {
		return sqlc.StorageHome{}, fmt.Errorf("home: validate deletion sentinel: %w", err)
	}
	row, err := q.CreateStorageHome(ctx, sqlc.CreateStorageHomeParams{ID: uuid.Must(uuid.NewV7()).String(), HomeKind: string(key.Kind), PrincipalKind: nullable(string(key.PrincipalKind)), PrincipalID: nullable(key.PrincipalID), AgentID: nullable(key.AgentID), StoreID: store.ID(), Locator: locator})
	if !errors.Is(err, pgx.ErrNoRows) {
		if err != nil {
			return sqlc.StorageHome{}, err
		}
		return row, nil
	}
	return getTx(ctx, q, key)
}

func getTx(ctx context.Context, q *sqlc.Queries, key Key) (sqlc.StorageHome, error) {
	switch key.Kind {
	case PrincipalHome:
		return q.GetPrincipalStorageHome(ctx, sqlc.GetPrincipalStorageHomeParams{PrincipalKind: nullable(string(key.PrincipalKind)), PrincipalID: nullable(key.PrincipalID)})
	case SystemAgentSkillRoot:
		return q.GetSystemAgentSkillStorageHome(ctx, nullable(key.AgentID))
	}
	return sqlc.StorageHome{}, fmt.Errorf("home: unsupported deletion sentinel %q", key.Kind)
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

type ownerPurgeArgs struct {
	OwnerKind OwnerKind `json:"owner_kind"`
	OwnerID   string    `json:"owner_id"`
	HomeIDs   []string  `json:"home_ids"`
	Actor     string    `json:"actor"`
}

func (ownerPurgeArgs) Kind() string { return "stella_home_owner_purge" }

func ownerPurgeInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Queue: HomePurgeQueue, MaxAttempts: ownerPurgeMaxAttempts, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateScheduled}}}
}

type ownerPurgeWorker struct {
	river.WorkerDefaults[ownerPurgeArgs]
	deletion *OwnerDeletion
}

func (w *ownerPurgeWorker) Work(ctx context.Context, j *river.Job[ownerPurgeArgs]) error {
	if err := w.deletion.fencer.FenceHomeOwner(ctx, j.Args.OwnerKind, j.Args.OwnerID); err != nil {
		return fmt.Errorf("home: fence %s owner %q: %w", j.Args.OwnerKind, j.Args.OwnerID, err)
	}
	// Do not trust enqueue argument order: child AgentHomes must lose their
	// bytes before the principal compatibility root on every retry.
	records := make([]Record, 0, len(j.Args.HomeIDs))
	var transient error
	for _, id := range j.Args.HomeIDs {
		record, err := w.deletion.registry.Record(ctx, id)
		if err != nil {
			// A malformed/stale batch member must remain retryable, but it must
			// not prevent independent Home bytes in this batch from being purged.
			transient = errors.Join(transient, fmt.Errorf("home: load purge record %q: %w", id, err))
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, k int) bool {
		left, right := purgeOrder(records[i]), purgeOrder(records[k])
		if left != right {
			return left < right
		}
		return records[i].ID < records[k].ID
	})
	for _, record := range records {
		if _, err := w.deletion.registry.Purge(ctx, record.ID, j.Args.Actor); err != nil {
			var physical *PhysicalPurgeError
			if errors.As(err, &physical) {
				continue // durably parked in purge_failed; admin must retry it.
			}
			transient = errors.Join(transient, fmt.Errorf("home %s: %w", record.ID, err))
		}
	}
	return transient
}

func purgeOrder(record Record) int {
	// AgentHomes nest under a principal compatibility root. Agent deletion has
	// no PrincipalHome, so its SystemAgent root follows all child AgentHomes.
	switch record.Key.Kind {
	case AgentHome:
		return 0
	case PrincipalHome:
		return 1
	case SystemAgentSkillRoot:
		return 2
	default:
		return 3
	}
}

func (d *OwnerDeletion) RegisterRiverWorker(workers *river.Workers) {
	river.AddWorker(workers, &ownerPurgeWorker{deletion: d})
}

func (d *OwnerDeletion) RiverQueueConfig() (string, river.QueueConfig) {
	return HomePurgeQueue, river.QueueConfig{MaxWorkers: 1}
}
