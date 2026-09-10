package sessionexecution

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func LeaseSessionIDForTest(l *Lease) string                                       { return l.sessionID }
func LeaseTokenForTest(l *Lease) string                                           { return l.token }
func StopHeartbeatForTest(l *Lease)                                               { l.once.Do(func() { close(l.stop) }); <-l.done }
func CancelLeaseForTest(l *Lease, err error)                                      { l.cancel(err) }
func SetFinishCommitForTest(s *Store, commit func(context.Context, pgx.Tx) error) { s.commit = commit }
