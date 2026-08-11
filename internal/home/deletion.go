package home

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// OwnerKind names the durable owner whose Homes are terminally tombstoned.
type OwnerKind string

const (
	OwnerUser  OwnerKind = "user"
	OwnerGroup OwnerKind = "group"
	OwnerAgent OwnerKind = "agent"
)

// OwnerFenceLease retains execution admission exclusion until owner deletion
// commits or rolls back. Commit is called only after the database commit;
// Release must be idempotent.
type OwnerFenceLease interface {
	Commit()
	Release()
}

// OwnerFenceAcquirer synchronously fences cached execution and retains the
// admission barrier needed to prevent publication or admission before commit.
type OwnerFenceAcquirer interface {
	AcquireHomeOwnerFence(context.Context, OwnerKind, string) (OwnerFenceLease, error)
}

// OwnerDeletion is the deep lifecycle boundary for destructive owner deletes.
// It owns the transaction that makes Homes unavailable and removes the owner.
type OwnerDeletion struct {
	db       *pgxpool.Pool
	registry *Registry
	fencer   OwnerFenceAcquirer
}

func NewOwnerDeletion(db *pgxpool.Pool, registry *Registry, fencer OwnerFenceAcquirer) (*OwnerDeletion, error) {
	if db == nil || registry == nil || fencer == nil {
		return nil, errors.New("home: owner deletion requires database, registry, and fencer")
	}
	return &OwnerDeletion{db: db, registry: registry, fencer: fencer}, nil
}

// DeleteGroup atomically tombstones all group Homes and deletes the group.
// The caller authorizes before reaching this boundary.
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
	if d == nil || d.db == nil || d.registry == nil || d.fencer == nil {
		return errors.New("home: owner deletion lifecycle is unavailable")
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(actor) == "" {
		return errors.New("home: owner deletion ID and actor are required")
	}
	// Reject missing/unsupported owners before fencing. This check is repeated
	// under row lock below; it only avoids an availability fence when there is
	// plainly no owner to delete.
	if err := d.ownerExists(ctx, sqlc.New(d.db), kind, id); err != nil {
		return err
	}
	// Admission barriers precede the Home owner gate everywhere. The retained
	// lease lets a turn already constructing a WorkspaceView finish first, then
	// prevents any later admission or service publication through DB commit.
	lease, err := d.fencer.AcquireHomeOwnerFence(ctx, kind, id)
	if err != nil {
		return fmt.Errorf("home: fence %s owner %q: %w", kind, id, err)
	}
	defer lease.Release()
	unlock, err := d.registry.lockOwnerKeys(ctx, []string{ownerLockKey(kind, id)})
	if err != nil {
		return err
	}
	defer unlock()
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
	if _, err := d.tombstoneHomes(ctx, q, kind, id, actor); err != nil {
		return err
	}
	if err := deleteOwner(ctx, q, id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		// A connection/context failure can lose the COMMIT acknowledgement even
		// though PostgreSQL committed. Reconcile before reopening admission. A
		// definite server rollback keeps the Agent service published and usable.
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return fmt.Errorf("home: commit owner deletion: %w", err)
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return fmt.Errorf("home: commit owner deletion: %w", err)
		}
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		reconcileErr := d.ownerExists(reconcileCtx, sqlc.New(d.db), kind, id)
		if errors.Is(reconcileErr, pgx.ErrNoRows) {
			lease.Commit()
			return nil
		}
		if reconcileErr != nil {
			// Fail closed on an unknown outcome. For Agent deletion Commit
			// unpublishes the already fenced service; for principals it is a no-op.
			lease.Commit()
			return fmt.Errorf("home: commit owner deletion: %w (reconcile: %w)", err, reconcileErr)
		}
		return fmt.Errorf("home: commit owner deletion: %w", err)
	}
	lease.Commit()
	return nil
}

func (d *OwnerDeletion) ownerExists(ctx context.Context, q *sqlc.Queries, kind OwnerKind, id string) error {
	var err error
	switch kind {
	case OwnerGroup:
		_, err = q.GetGroupStateByID(ctx, id)
	case OwnerAgent:
		_, err = q.GetAgent(ctx, id)
	case OwnerUser:
		_, err = q.GetAuthUser(ctx, id)
	default:
		return fmt.Errorf("home: unsupported deletion owner %q", kind)
	}
	if err != nil {
		return fmt.Errorf("home: validate %s owner: %w", kind, err)
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
		if State(row.State) == StateTombstoned {
			// AgentHomes are shared by their principal and Agent deletion sets.
			// A terminal row belongs to the first valid owner delete; do not make
			// that prior audit block deletion of the other owner.
			continue
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
