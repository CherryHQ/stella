// Package controlsession owns the one pool-external serialized PostgreSQL
// session each stellad process uses for notifications, health, and ingress
// advisory-lock leadership.
package controlsession

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const notificationChannel = "stella_runtime_control"

type Session struct {
	pool        *pgxpool.Pool
	config      *pgx.ConnConfig
	initialConn *pgx.Conn
	bootID      string
	ctx         context.Context
	cancel      context.CancelCauseFunc
	done        chan struct{}
	ops         chan operation
	once        sync.Once
	closeErr    error
}

type operation struct {
	ctx context.Context
	fn  func(context.Context, *pgx.Conn) (bool, error)
	res chan operationResult
}

type operationResult struct {
	ok    bool
	epoch context.Context
	err   error
}

func Open(ctx context.Context, pool *pgxpool.Pool, bootID string) (*Session, error) {
	if pool == nil || bootID == "" {
		return nil, errors.New("control session requires database and boot ID")
	}
	config := pool.Config().ConnConfig.Copy()
	config.RuntimeParams["application_name"] = "stellad-control"
	conn, err := openConnection(ctx, config, bootID, true)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	s := &Session{
		pool: pool, config: config, initialConn: conn, bootID: bootID,
		ctx: runCtx, cancel: cancel, done: make(chan struct{}), ops: make(chan operation),
	}
	go s.run()
	return s, nil
}

func openConnection(ctx context.Context, config *pgx.ConnConfig, bootID string, initial bool) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect PostgreSQL control session: %w", err)
	}
	fail := func(err error) (*pgx.Conn, error) {
		_ = conn.Close(context.Background())
		return nil, err
	}
	if err := rejectTransactionPooling(ctx, conn, bootID); err != nil {
		return fail(err)
	}
	if _, err := conn.Exec(ctx, "LISTEN "+notificationChannel); err != nil {
		return fail(fmt.Errorf("listen on control session: %w", err))
	}
	var backendPID int64
	if err := conn.QueryRow(ctx, "SELECT pg_backend_pid()::bigint").Scan(&backendPID); err != nil {
		return fail(fmt.Errorf("read control backend pid: %w", err))
	}
	q := sqlc.New(conn)
	pid := pgtype.Int8{Int64: backendPID, Valid: true}
	if initial {
		if _, err := q.CreateExecutorBoot(ctx, sqlc.CreateExecutorBootParams{ID: bootID, ControlBackendPid: pid}); err != nil {
			return fail(fmt.Errorf("register executor boot: %w", err))
		}
	} else {
		rows, err := q.ReconnectExecutorBoot(ctx, sqlc.ReconnectExecutorBootParams{ID: bootID, ControlBackendPid: pid})
		if err != nil {
			return fail(fmt.Errorf("re-register executor boot: %w", err))
		}
		if rows != 1 {
			return fail(errors.New("re-register executor boot: boot is no longer running"))
		}
	}
	return conn, nil
}

// rejectTransactionPooling proves that session advisory locks and backend
// identity survive transaction boundaries. Those are the semantics LISTEN and
// pull/WebSocket leadership require; a transaction-pooling proxy fails closed.
func rejectTransactionPooling(ctx context.Context, conn *pgx.Conn, bootID string) error {
	h := fnv.New64a()
	_, _ = h.Write([]byte("stella-control:" + bootID))
	key := int64(h.Sum64())
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return fmt.Errorf("control session advisory-lock probe: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", key) }()
	var held bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE pid = pg_backend_pid() AND locktype = 'advisory' AND granted
		)`).Scan(&held); err != nil {
		return fmt.Errorf("control session affinity probe: %w", err)
	}
	return requireBackendAffinity(held)
}

func requireBackendAffinity(lockHeld bool) error {
	if lockHeld {
		return nil
	}
	return errors.New("transaction-pooling PostgreSQL proxies are unsupported: control session has no backend affinity")
}

func (s *Session) Context() context.Context { return s.ctx }

func (s *Session) run() {
	defer close(s.done)
	conn := s.initialConn
	s.initialConn = nil
	for s.ctx.Err() == nil {
		epoch, cancelEpoch := context.WithCancelCause(s.ctx)
		err := s.serveConnection(conn, epoch, cancelEpoch)
		cancelEpoch(err)
		_ = conn.Close(context.Background())
		if s.ctx.Err() != nil {
			return
		}
		conn = nil
		backoff := 100 * time.Millisecond
		for s.ctx.Err() == nil {
			connectCtx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
			next, connectErr := openConnection(connectCtx, s.config, s.bootID, false)
			cancel()
			if connectErr == nil {
				conn = next
				break
			}
			timer := time.NewTimer(backoff)
			select {
			case <-s.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < 2*time.Second {
				backoff *= 2
			}
		}
		if conn == nil {
			return
		}
	}
}

func (s *Session) serveConnection(conn *pgx.Conn, epoch context.Context, cancelEpoch context.CancelCauseFunc) error {
	q := sqlc.New(conn)
	lastHeartbeat := time.Time{}
	for epoch.Err() == nil {
		select {
		case op := <-s.ops:
			ok, err := op.fn(op.ctx, conn)
			if err != nil {
				pingCtx, cancel := context.WithTimeout(epoch, time.Second)
				pingErr := conn.Ping(pingCtx)
				cancel()
				if pingErr != nil {
					cancelEpoch(fmt.Errorf("PostgreSQL control session lost: %w", pingErr))
				}
			}
			op.res <- operationResult{ok: ok, epoch: epoch, err: err}
			if epoch.Err() != nil {
				return context.Cause(epoch)
			}
			continue
		default:
		}
		waitCtx, cancel := context.WithTimeout(epoch, 200*time.Millisecond)
		_, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("PostgreSQL control session lost: %w", err)
		}
		if epoch.Err() != nil {
			return context.Cause(epoch)
		}
		if time.Since(lastHeartbeat) >= time.Second {
			rows, err := q.HeartbeatExecutorBoot(epoch, s.bootID)
			if err != nil {
				return fmt.Errorf("PostgreSQL control session heartbeat failed: %w", err)
			}
			if rows != 1 {
				return errors.New("PostgreSQL control session heartbeat rejected: boot is no longer running")
			}
			lastHeartbeat = time.Now()
		}
	}
	return context.Cause(epoch)
}

func (s *Session) execute(ctx context.Context, fn func(context.Context, *pgx.Conn) (bool, error)) (operationResult, error) {
	if s == nil {
		return operationResult{}, errors.New("control session is not configured")
	}
	op := operation{ctx: ctx, fn: fn, res: make(chan operationResult, 1)}
	select {
	case s.ops <- op:
	case <-ctx.Done():
		return operationResult{}, ctx.Err()
	case <-s.ctx.Done():
		return operationResult{}, context.Cause(s.ctx)
	}
	select {
	case result := <-op.res:
		return result, nil
	case <-ctx.Done():
		return operationResult{}, ctx.Err()
	case <-s.ctx.Done():
		return operationResult{}, context.Cause(s.ctx)
	}
}

func leadershipKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("stella-ingress:" + name))
	return int64(h.Sum64())
}

// RunLeader waits for a session-scoped advisory lock, starts listener work only
// while this control connection owns it, and releases it after that work has
// stopped. Connection loss cancels the callback immediately and PostgreSQL
// releases the lock with the dead backend.
func (s *Session) RunLeader(ctx context.Context, name string, run func(context.Context)) error {
	if name == "" || run == nil {
		return errors.New("control-session leadership requires a name and callback")
	}
	key := leadershipKey(name)
	for ctx.Err() == nil && s.ctx.Err() == nil {
		result, err := s.execute(ctx, func(opCtx context.Context, conn *pgx.Conn) (bool, error) {
			var ok bool
			err := conn.QueryRow(opCtx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok)
			return ok, err
		})
		if err != nil {
			return err
		}
		if result.err != nil {
			if result.epoch != nil && result.epoch.Err() != nil {
				continue
			}
			return result.err
		}
		if result.ok {
			leaderCtx, cancel := context.WithCancelCause(ctx)
			stopOnLoss := context.AfterFunc(result.epoch, func() { cancel(context.Cause(result.epoch)) })
			run(leaderCtx)
			<-leaderCtx.Done()
			stopOnLoss()
			cancel(context.Canceled)
			if result.epoch.Err() == nil && s.ctx.Err() == nil {
				unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
				unlockResult, unlockErr := s.execute(unlockCtx, func(opCtx context.Context, conn *pgx.Conn) (bool, error) {
					var ok bool
					err := conn.QueryRow(opCtx, "SELECT pg_advisory_unlock($1)", key).Scan(&ok)
					return ok, err
				})
				unlockCancel()
				if unlockErr != nil {
					return unlockErr
				}
				if unlockResult.err != nil {
					return unlockResult.err
				}
			}
			if result.epoch.Err() != nil && ctx.Err() == nil && s.ctx.Err() == nil {
				// Connection loss already canceled and drained the listener. Wait for
				// the supervisor to reconnect, then perform a full lock scan.
				continue
			}
			return context.Cause(leaderCtx)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-s.ctx.Done():
			timer.Stop()
			return context.Cause(s.ctx)
		case <-timer.C:
		}
	}
	return errors.Join(ctx.Err(), context.Cause(s.ctx))
}

// Close drains listener ownership before closing the session. Session-scoped
// advisory locks release only after listeners have stopped.
func (s *Session) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.once.Do(func() {
		s.cancel(context.Canceled)
		<-s.done
		_, s.closeErr = sqlc.New(s.pool).DrainExecutorBoot(ctx, s.bootID)
	})
	closeErr = s.closeErr
	return closeErr
}
