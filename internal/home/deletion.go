package home

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type OwnerKind string

const (
	OwnerUser  OwnerKind = "user"
	OwnerGroup OwnerKind = "group"
	OwnerAgent OwnerKind = "agent"
)

type OwnerFenceLease interface {
	Commit()
	Release()
}
type OwnerFenceAcquirer interface {
	AcquireHomeOwnerFence(context.Context, OwnerKind, string) (OwnerFenceLease, error)
}

// MediaPurger drops an owner's immutable session-media objects, which no
// database cascade can reach. It is optional: a deployment without session
// media configured simply has nothing to purge.
type MediaPurger interface {
	PurgeOwner(context.Context, OwnerKind, string) error
}

type OwnerDeletion struct {
	db             *pgxpool.Pool
	manager        *WorkspaceManager
	fencer         OwnerFenceAcquirer
	media          MediaPurger
	commitTx       func(context.Context, pgx.Tx) error
	reconcileOwner func(context.Context, OwnerKind, string) error
}

type OwnerDeletionOption func(*OwnerDeletion)

// WithMediaPurger attaches blob cleanup to owner deletion.
func WithMediaPurger(media MediaPurger) OwnerDeletionOption {
	return func(d *OwnerDeletion) { d.media = media }
}

func NewOwnerDeletion(db *pgxpool.Pool, manager *WorkspaceManager, fencer OwnerFenceAcquirer, opts ...OwnerDeletionOption) (*OwnerDeletion, error) {
	if db == nil || manager == nil || fencer == nil {
		return nil, errors.New("home: owner deletion requires database, workspace manager, and fencer")
	}
	d := &OwnerDeletion{db: db, manager: manager, fencer: fencer}
	for _, opt := range opts {
		opt(d)
	}
	return d, nil
}

// purgeMedia runs only once the owner row is definitively gone, and its failure
// never changes the outcome. The order is the whole point: purging first would
// destroy a live owner's images whenever the transaction then rolled back,
// while purging after can only leave unreferenced blobs behind.
//
// Ceiling: nothing reclaims those leftovers. A crash between the commit and this
// call, or a partial purge failure, strands the owner's whole prefix — the
// orphan sweep works from ctx_media rows, and the cascade has already deleted
// them. Add prefix-level reconciliation when stranded owner prefixes make disk
// usage visible.
func (d *OwnerDeletion) purgeMedia(ctx context.Context, kind OwnerKind, id string) {
	if d.media == nil {
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := d.media.PurgeOwner(pctx, kind, id); err != nil {
		slog.Warn("purge owner session media", "kind", kind, "owner_id", id, "error", err)
	}
}

func (d *OwnerDeletion) DeleteGroup(ctx context.Context, id, actor string) error {
	return d.delete(ctx, OwnerGroup, id, actor, func(ctx context.Context, q *sqlc.Queries, id string) error { return q.DeleteGroupState(ctx, id) })
}

func (d *OwnerDeletion) DeleteAgent(ctx context.Context, id, actor string) error {
	return d.delete(ctx, OwnerAgent, id, actor, func(ctx context.Context, q *sqlc.Queries, id string) error { return q.DeleteAgent(ctx, id) })
}

func (d *OwnerDeletion) DeleteUser(ctx context.Context, id, actor string) error {
	return d.delete(ctx, OwnerUser, id, actor, func(ctx context.Context, q *sqlc.Queries, id string) error { return q.DeleteAuthUser(ctx, id) })
}

type deleteFunc func(context.Context, *sqlc.Queries, string) error

func (d *OwnerDeletion) delete(ctx context.Context, kind OwnerKind, id, actor string, del deleteFunc) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(actor) == "" {
		return errors.New("home: owner deletion ID and actor required")
	}
	if err := d.exists(ctx, sqlc.New(d.db), kind, id, false); err != nil {
		return err
	}
	// Lock order is process lifecycle fence -> local owner gate -> owner DB row/transaction.
	lease, err := d.fencer.AcquireHomeOwnerFence(ctx, kind, id)
	if err != nil {
		return fmt.Errorf("home: fence owner: %w", err)
	}
	defer lease.Release()
	unlock, err := d.manager.ownerGate(ctx, kind, id)
	if err != nil {
		return err
	}
	defer unlock()
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	if err = d.exists(ctx, q, kind, id, true); err != nil {
		return err
	}
	if err = del(ctx, q, id); err != nil {
		return err
	}
	commitTx := d.commitTx
	if commitTx == nil {
		commitTx = func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }
	}
	if err = commitTx(ctx, tx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return fmt.Errorf("home: commit owner deletion: %w", err)
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			return fmt.Errorf("home: commit owner deletion: %w", err)
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		reconcileOwner := d.reconcileOwner
		if reconcileOwner == nil {
			reconcileOwner = func(ctx context.Context, kind OwnerKind, id string) error {
				return d.exists(ctx, sqlc.New(d.db), kind, id, false)
			}
		}
		e := reconcileOwner(rctx, kind, id)
		if errors.Is(e, pgx.ErrNoRows) {
			d.purgeMedia(ctx, kind, id)
			lease.Commit()
			return nil
		}
		if e != nil {
			lease.Commit()
			return fmt.Errorf("home: unknown commit outcome: %w", err)
		}
		return err
	}
	d.purgeMedia(ctx, kind, id)
	lease.Commit()
	return nil
}

func (d *OwnerDeletion) exists(ctx context.Context, q *sqlc.Queries, kind OwnerKind, id string, lock bool) error {
	var err error
	switch kind {
	case OwnerUser:
		if lock {
			_, err = q.GetAuthUserForUpdate(ctx, id)
		} else {
			_, err = q.GetAuthUser(ctx, id)
		}
	case OwnerGroup:
		if lock {
			_, err = q.GetGroupStateByIDForUpdate(ctx, id)
		} else {
			_, err = q.GetGroupStateByID(ctx, id)
		}
	case OwnerAgent:
		if lock {
			_, err = q.GetAgentForUpdate(ctx, id)
		} else {
			_, err = q.GetAgent(ctx, id)
		}
	default:
		return errors.New("home: unsupported owner")
	}
	return err
}
