package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
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
type OwnerDeletion struct {
	db             *pgxpool.Pool
	manager        *WorkspaceManager
	fencer         OwnerFenceAcquirer
	commitTx       func(context.Context, pgx.Tx) error
	reconcileOwner func(context.Context, OwnerKind, string) error
}

func NewOwnerDeletion(db *pgxpool.Pool, manager *WorkspaceManager, fencer OwnerFenceAcquirer) (*OwnerDeletion, error) {
	if db == nil || manager == nil || fencer == nil {
		return nil, errors.New("home: owner deletion requires database, workspace manager, and fencer")
	}
	return &OwnerDeletion{db: db, manager: manager, fencer: fencer}, nil
}

func (d *OwnerDeletion) DeleteGroup(ctx context.Context, id, actor string) error {
	return d.delete(ctx, OwnerGroup, id, actor, nil, func(ctx context.Context, q *sqlc.Queries, id string) error { return q.DeleteGroupState(ctx, id) })
}

func (d *OwnerDeletion) DeleteAgent(ctx context.Context, id, actor string) error {
	return d.delete(ctx, OwnerAgent, id, actor, nil, func(ctx context.Context, q *sqlc.Queries, id string) error { return q.DeleteAgent(ctx, id) })
}

// DeleteAgentIfUnchanged compares the locked Agent row and deletes it inside the
// same lifecycle fence and transaction. The fence must be acquired before the
// row disappears so no new workspace or runner work can race the deletion.
func (d *OwnerDeletion) DeleteAgentIfUnchanged(ctx context.Context, id, actor string, expected config.Agent) error {
	check := func(ctx context.Context, q *sqlc.Queries, id string) error {
		row, err := q.GetAgentForUpdate(ctx, id)
		if err != nil {
			return err
		}
		current, err := deletionAgentFromDB(row)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, expected) {
			return config.ErrAgentChanged
		}
		return nil
	}
	return d.delete(ctx, OwnerAgent, id, actor, check, func(ctx context.Context, q *sqlc.Queries, id string) error { return q.DeleteAgent(ctx, id) })
}

func (d *OwnerDeletion) DeleteUser(ctx context.Context, id, actor string) error {
	return d.delete(ctx, OwnerUser, id, actor, nil, func(ctx context.Context, q *sqlc.Queries, id string) error { return q.DeleteAuthUser(ctx, id) })
}

type (
	deleteCheck func(context.Context, *sqlc.Queries, string) error
	deleteFunc  func(context.Context, *sqlc.Queries, string) error
)

func (d *OwnerDeletion) delete(ctx context.Context, kind OwnerKind, id, actor string, check deleteCheck, del deleteFunc) error {
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
	if check != nil {
		if err = check(ctx, q, id); err != nil {
			return err
		}
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
			lease.Commit()
			return nil
		}
		if e != nil {
			lease.Commit()
			return fmt.Errorf("home: unknown commit outcome: %w", err)
		}
		return err
	}
	lease.Commit()
	return nil
}

func deletionAgentFromDB(row sqlc.Agent) (config.Agent, error) {
	var sandbox config.SandboxConfig
	if len(row.Sandbox) > 0 {
		if err := json.Unmarshal(row.Sandbox, &sandbox); err != nil {
			return config.Agent{}, fmt.Errorf("home: parse Agent %q sandbox: %w", row.ID, err)
		}
	}
	scope := row.Scope
	if scope == "" {
		scope = config.AgentScopeSystem
	}
	return config.Agent{
		ID: row.ID, Name: row.Name, Model: row.Model, ModelThinking: row.ModelThinking,
		ModelStrong: row.ModelStrong, ModelStrongThinking: row.ModelStrongThinking,
		ModelFast: row.ModelFast, ModelFastThinking: row.ModelFastThinking,
		SystemPrompt: row.SystemPrompt, Soul: row.Soul, Workspace: row.Workspace,
		Sandbox: sandbox, Scope: scope, CreatorID: row.CreatorID, Enabled: row.Enabled,
	}, nil
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
