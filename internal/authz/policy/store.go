package policy

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// policyStore is the PostgreSQL persistence adapter for the authorization policy
// core. It owns the revision read, the consistent active-row reload, and the
// mutation transaction that bumps the commit-ordered revision counter. It is the
// only place in this package that touches the database, and is unexported: the
// Authorizer and the mutation Service share one instance inside the package,
// never across it.
type policyStore struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

// newPolicyStore builds a policyStore over a pgx pool.
func newPolicyStore(pool *pgxpool.Pool) *policyStore {
	return &policyStore{pool: pool, q: sqlc.New(pool)}
}

// revision reads the single authoritative revision. This is the one lightweight
// read every protected use case performs at Begin.
func (s *policyStore) revision(ctx context.Context) (int64, error) {
	rev, err := s.q.GetAuthzPolicyRevision(ctx)
	if err != nil {
		return 0, fmt.Errorf("authz/policy: read revision: %w", err)
	}
	return rev, nil
}

// loadForReload reads the revision and the active policy rows inside ONE short
// REPEATABLE READ, read-only snapshot, so the revision and the rows can never be
// torn across a concurrent committed mutation: the reload sees either the whole
// mutation or none of it. It compiles nothing — the transaction is held only for
// the two reads and then released.
func (s *policyStore) loadForReload(ctx context.Context) (int64, []sqlc.AuthzPolicy, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("authz/policy: begin reload snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	rev, err := qtx.GetAuthzPolicyRevision(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("authz/policy: reload revision: %w", err)
	}
	rows, err := qtx.ListActiveAuthzPolicy(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("authz/policy: reload active policies: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, fmt.Errorf("authz/policy: commit reload snapshot: %w", err)
	}
	return rev, rows, nil
}

// mutate runs fn inside a mutation transaction that FIRST locks and advances the
// single revision counter (UPDATE ... RETURNING takes the row lock), THEN lets
// fn write the policy, THEN commits. Because the counter row is locked before
// any policy write, two concurrent mutations serialize on that lock and their
// revisions follow commit order; there is no sequence/nextval. fn receives the
// transaction-scoped queries and the freshly assigned revision.
//
// beforeCommit, when non-nil, runs after fn and before Commit; it exists purely
// for deterministic tests that need to observe the pre-commit window. It is nil
// in production.
func (s *policyStore) mutate(
	ctx context.Context,
	beforeCommit func(),
	fn func(qtx *sqlc.Queries, revision int64) error,
) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("authz/policy: begin mutation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)

	// Lock + advance the counter BEFORE the policy write. This is the
	// serialization point: a concurrent mutation blocks here until we commit.
	rev, err := qtx.BumpAuthzPolicyRevision(ctx)
	if err != nil {
		return 0, fmt.Errorf("authz/policy: bump revision: %w", err)
	}

	if err := fn(qtx, rev); err != nil {
		return 0, err
	}

	if beforeCommit != nil {
		beforeCommit()
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("authz/policy: commit mutation: %w", err)
	}
	return rev, nil
}
