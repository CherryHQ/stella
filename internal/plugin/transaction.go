package plugin

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MutationFence holds the process-wide admission boundary while a plugin
// mutation is committed and its published runners are retired.
type MutationFence func(context.Context, func() error) error

var ErrCommitOutcomeUnknown = errors.New("plugin: commit outcome unknown")

func classifyCommitError(err error) error {
	return ClassifyMutationError(err)
}

// ClassifyMutationError applies the conservative outcome rule to writes whose
// completion boundary returned an error. Only errors known to happen before
// sending data, or PostgreSQL's explicit rollback signal, are definitive.
// Server errors remain conservative because transaction_resolution_unknown and
// statement_completion_unknown are valid PostgreSQL errors too.
func ClassifyMutationError(err error) error {
	if err == nil || errors.Is(err, pgx.ErrTxCommitRollback) || pgconn.SafeToRetry(err) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrCommitOutcomeUnknown, err)
}
