package plugin

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type safeToRetryTestError struct{}

func (safeToRetryTestError) Error() string     { return "before send" }
func (safeToRetryTestError) SafeToRetry() bool { return true }

func TestClassifyMutationErrorKeepsDefinitiveOutcomes(t *testing.T) {
	for _, err := range []error{nil, pgx.ErrTxCommitRollback, safeToRetryTestError{}} {
		if got := ClassifyMutationError(err); !errors.Is(got, err) {
			t.Fatalf("definitive error %v classified as %v", err, got)
		}
	}
}

func TestClassifyMutationErrorMarksPostSendFailureUnknown(t *testing.T) {
	transportErr := errors.New("connection reset after write")
	if err := ClassifyMutationError(transportErr); !errors.Is(err, ErrCommitOutcomeUnknown) || !errors.Is(err, transportErr) {
		t.Fatalf("transport error = %v, want unknown outcome wrapping transport error", err)
	}
}

func TestClassifyMutationErrorMarksAmbiguousServerErrorsUnknown(t *testing.T) {
	for _, code := range []string{"23505", "08007", "40003"} {
		serverErr := &pgconn.PgError{Code: code, Message: "server response"}
		err := ClassifyMutationError(serverErr)
		if !errors.Is(err, serverErr) || !errors.Is(err, ErrCommitOutcomeUnknown) {
			t.Fatalf("server error %s = %v, want unknown outcome wrapping server error", code, err)
		}
	}
}
