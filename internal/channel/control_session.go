package channel

// This file owns the process-wide PostgreSQL control connection used by
// process-local channel listeners.  It is deliberately separate from the
// pool: LISTEN, session advisory locks, and backend identity are all session
// state, so borrowing a pool connection would make the leadership fence
// disappear as soon as the connection is released.

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ControlNotificationChannel is the durable-state wakeup channel. Payloads
// are hints only; consumers must reconcile their durable rows.
const ControlNotificationChannel = "stella_runtime_control"

var (
	// ErrControlSessionClosed means the process-wide control session has been
	// closed and cannot accept another serialized operation.
	ErrControlSessionClosed = errors.New("channel control session is closed")
	// ErrTransactionPooling means the configured endpoint did not preserve
	// PostgreSQL session affinity. LISTEN and session advisory locks cannot be
	// made safe over transaction pooling.
	ErrTransactionPooling = errors.New("transaction-pooling PostgreSQL proxies are unsupported")
)

// ControlSessionOptions carries deployment knowledge that PostgreSQL cannot
// reveal reliably through an application connection. PoolMode should be set
// when the deployment explicitly knows it is behind a pooler; transaction and
// statement pooling are rejected before the control connection is opened.
// Leaving it empty preserves direct-PostgreSQL deployments, but the affinity
// probe below remains a necessary per-connection check rather than a proof
// that an unknown proxy will never rebind a statement.
type ControlSessionOptions struct {
	PoolMode string
}

// ControlSession is the one physical PostgreSQL connection owned by a Stella
// replica for channel wakeups, health, and listener leadership. All operations
// on the connection run through one goroutine. A reconnect gets a new backend
// and therefore starts a new leadership epoch; callers must rebuild listeners
// from durable state after the epoch changes.
type ControlSession struct {
	config *pgx.ConnConfig
	marker string

	initialConn *pgx.Conn
	ctx         context.Context
	cancel      context.CancelCauseFunc
	parentStop  func() bool
	done        chan struct{}
	ops         chan controlOperation
	notices     chan *pgconn.Notification

	leaders       sync.WaitGroup
	closeMu       sync.Mutex
	leaderCancels map[uint64]context.CancelCauseFunc
	leaderNames   map[string]struct{}
	nameChanges   chan struct{}
	nextLeaderID  uint64
	closeStarted  bool
	closeDone     chan struct{}
	closeErr      error
}

type controlOperation struct {
	ctx context.Context
	fn  func(context.Context, *pgx.Conn) (any, error)
	res chan controlResult
}

type controlResult struct {
	value any
	epoch context.Context
	err   error
}

// OpenControlSession creates a pool-external physical connection. The pool is
// used only as the source of the already parsed connection configuration; no
// connection is acquired from or returned to the pool.
func OpenControlSession(ctx context.Context, pool *pgxpool.Pool) (*ControlSession, error) {
	return OpenControlSessionWithOptions(ctx, pool, ControlSessionOptions{})
}

// OpenControlSessionWithOptions creates a pool-external physical connection
// with explicit deployment pooler knowledge when available.
func OpenControlSessionWithOptions(ctx context.Context, pool *pgxpool.Pool, options ControlSessionOptions) (*ControlSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if pool == nil || pool.Config() == nil || pool.Config().ConnConfig == nil {
		return nil, errors.New("channel control session requires a database pool")
	}
	config := pool.Config().ConnConfig.Copy()
	if err := rejectKnownPoolMode(config, options.PoolMode); err != nil {
		return nil, err
	}
	if config.RuntimeParams == nil {
		config.RuntimeParams = map[string]string{}
	}
	marker := "stellad-control-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	config.RuntimeParams["application_name"] = marker

	conn, err := openControlConnection(ctx, config, marker)
	if err != nil {
		return nil, err
	}
	// The physical loop has its own lifecycle context. The caller context is
	// observed through parentStop so Close can cancel listeners and wait for
	// their cleanup before it tears down the session that owns their locks.
	runCtx, cancel := context.WithCancelCause(context.Background())
	s := &ControlSession{
		config:        config,
		marker:        marker,
		initialConn:   conn,
		ctx:           runCtx,
		cancel:        cancel,
		done:          make(chan struct{}),
		ops:           make(chan controlOperation),
		notices:       make(chan *pgconn.Notification, 64),
		leaderCancels: map[uint64]context.CancelCauseFunc{},
		leaderNames:   map[string]struct{}{},
		nameChanges:   make(chan struct{}),
		closeDone:     make(chan struct{}),
	}
	s.parentStop = context.AfterFunc(ctx, func() { _ = s.Close(context.Background()) })
	go s.run()
	return s, nil
}

func rejectKnownPoolMode(config *pgx.ConnConfig, configured string) error {
	mode := strings.TrimSpace(configured)
	if mode == "" && config != nil {
		for _, key := range []string{"stella_pool_mode", "pgbouncer_pool_mode", "pool_mode"} {
			if value := config.RuntimeParams[key]; value != "" {
				mode = value
				break
			}
		}
	}
	switch strings.ToLower(mode) {
	case "transaction", "statement":
		return fmt.Errorf("%w: configured pool mode %q cannot preserve LISTEN or session advisory locks; use session pooling or a direct PostgreSQL control DSN", ErrTransactionPooling, mode)
	default:
		return nil
	}
}

func openControlConnection(ctx context.Context, config *pgx.ConnConfig, marker string) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect PostgreSQL control session: %w", err)
	}
	fail := func(err error) (*pgx.Conn, error) {
		_ = conn.Close(context.Background())
		return nil, err
	}
	if err := probeSessionAffinity(ctx, conn, marker); err != nil {
		return fail(err)
	}
	if _, err := conn.Exec(ctx, "LISTEN "+ControlNotificationChannel); err != nil {
		return fail(fmt.Errorf("listen on control session: %w", err))
	}
	return conn, nil
}

// probeSessionAffinity checks backend identity and lock persistence across two
// statements on this connection. This is a necessary startup check, not a
// complete proof against an unknown transaction-pooling proxy: a proxy can
// happen to route both statements to the same backend and pass this probe.
// Deployments that know their pool mode must reject transaction/statement mode
// through ControlSessionOptions or use a direct/session-pooled control DSN. The
// lock is intentionally released before the probe returns, so an unsuccessful
// startup cannot strand state.
func probeSessionAffinity(ctx context.Context, conn *pgx.Conn, marker string) error {
	before, applicationName, err := readSessionIdentity(ctx, conn)
	if err != nil {
		return fmt.Errorf("read control backend pid: %w", err)
	}
	if applicationName != marker {
		return fmt.Errorf("%w: application_name changed during session startup (want=%q got=%q)", ErrTransactionPooling, marker, applicationName)
	}
	key := controlProbeKey(before)
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return fmt.Errorf("control session advisory-lock probe: %w: %w", ErrTransactionPooling, err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", key)
	}()

	var after int64
	var held bool
	if err := conn.QueryRow(ctx, `
		SELECT pg_backend_pid()::bigint,
		       EXISTS (
			       SELECT 1 FROM pg_locks
			       WHERE pid = pg_backend_pid()
			         AND locktype = 'advisory'
			         AND granted
			         AND classid = (($1::bigint >> 32) & 4294967295)::oid
			         AND objid = ($1::bigint & 4294967295)::oid
			         AND objsubid = 1
		       )`, key).Scan(&after, &held); err != nil {
		return fmt.Errorf("control session affinity probe: %w: %w", ErrTransactionPooling, err)
	}
	if before != after || !held {
		return fmt.Errorf("%w: backend affinity changed during session probe (before=%d after=%d held=%t)", ErrTransactionPooling, before, after, held)
	}
	return nil
}

func readSessionIdentity(ctx context.Context, conn *pgx.Conn) (int64, string, error) {
	var pid int64
	var applicationName string
	err := conn.QueryRow(ctx, "SELECT pg_backend_pid()::bigint, current_setting('application_name')").Scan(&pid, &applicationName)
	return pid, applicationName, err
}

func controlProbeKey(backendPID int64) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("stella-control-probe:"))
	var b [8]byte
	for i := range b {
		b[i] = byte(backendPID >> (8 * i))
	}
	_, _ = h.Write(b[:])
	return int64(h.Sum64())
}

// Context is canceled when this session is closed. A physical connection loss
// cancels only the current leadership epoch exposed to RunLeader; the session
// itself reconnects and establishes a new epoch.
func (s *ControlSession) Context() context.Context {
	if s == nil {
		return context.Background()
	}
	return s.ctx
}

// Notifications returns best-effort wakeups received on the control channel.
// Notifications are advisory only. Consumers must reconcile from durable
// state after reconnects and when this bounded buffer is full.
func (s *ControlSession) Notifications() <-chan *pgconn.Notification {
	if s == nil {
		return nil
	}
	return s.notices
}

// BackendPID returns the current backend PID, or zero before the first
// reconnect has completed. It is diagnostic only and must not be used as an
// ownership fence.
func (s *ControlSession) BackendPID(ctx context.Context) (int64, error) {
	value, err := s.execute(ctx, func(opCtx context.Context, conn *pgx.Conn) (any, error) {
		var pid int64
		err := conn.QueryRow(opCtx, "SELECT pg_backend_pid()::bigint").Scan(&pid)
		return pid, err
	})
	if err != nil {
		return 0, err
	}
	if value.value == nil {
		return 0, errors.New("control session returned no backend pid")
	}
	pid, ok := value.value.(int64)
	if !ok {
		return 0, fmt.Errorf("control session returned backend pid of type %T", value.value)
	}
	return pid, value.err
}

// Ping executes a health check on the control connection. It is serialized
// with leadership operations and therefore cannot accidentally use a pool
// connection.
func (s *ControlSession) Ping(ctx context.Context) error {
	result, err := s.execute(ctx, func(opCtx context.Context, conn *pgx.Conn) (any, error) {
		return nil, conn.Ping(opCtx)
	})
	if err != nil {
		return err
	}
	return result.err
}

func (s *ControlSession) run() {
	defer close(s.done)
	defer close(s.notices)
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
			next, connectErr := openControlConnection(connectCtx, s.config, s.marker)
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

func (s *ControlSession) serveConnection(conn *pgx.Conn, epoch context.Context, cancelEpoch context.CancelCauseFunc) error {
	backendPID, applicationName, err := readSessionIdentity(epoch, conn)
	if err != nil {
		return fmt.Errorf("read control session identity: %w", err)
	}
	if applicationName != s.marker {
		return fmt.Errorf("%w: control session application_name changed (want=%q got=%q)", ErrTransactionPooling, s.marker, applicationName)
	}
	lastHeartbeat := time.Time{}
	for epoch.Err() == nil {
		select {
		case op := <-s.ops:
			value, err := op.fn(op.ctx, conn)
			if err != nil {
				pingCtx, cancel := context.WithTimeout(epoch, time.Second)
				pingErr := conn.Ping(pingCtx)
				cancel()
				if pingErr != nil {
					cancelEpoch(fmt.Errorf("PostgreSQL control session lost: %w", pingErr))
				}
			}
			op.res <- controlResult{value: value, epoch: epoch, err: err}
			if epoch.Err() != nil {
				return context.Cause(epoch)
			}
			continue
		default:
		}

		waitCtx, cancel := context.WithTimeout(epoch, 200*time.Millisecond)
		notification, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("PostgreSQL control session lost: %w", err)
		}
		if notification != nil {
			select {
			case s.notices <- notification:
			default:
				// Wakeups are disposable. Full scans and heartbeat repair a
				// dropped notification, so never let a slow consumer stall the
				// sole control connection.
			}
		}
		if epoch.Err() != nil {
			return context.Cause(epoch)
		}
		if time.Since(lastHeartbeat) >= time.Second {
			if err := conn.Ping(epoch); err != nil {
				return fmt.Errorf("PostgreSQL control session heartbeat failed: %w", err)
			}
			currentPID, currentApplicationName, err := readSessionIdentity(epoch, conn)
			if err != nil {
				return fmt.Errorf("PostgreSQL control session identity heartbeat failed: %w", err)
			}
			if currentPID != backendPID || currentApplicationName != s.marker {
				return fmt.Errorf("%w: control session identity changed (pid %d/%d application_name %q/%q)", ErrTransactionPooling, backendPID, currentPID, s.marker, currentApplicationName)
			}
			lastHeartbeat = time.Now().UTC()
		}
	}
	return context.Cause(epoch)
}

func (s *ControlSession) execute(ctx context.Context, fn func(context.Context, *pgx.Conn) (any, error)) (controlResult, error) {
	if s == nil {
		return controlResult{}, ErrControlSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.closeMu.Lock()
	closed := s.closeStarted
	s.closeMu.Unlock()
	if closed {
		return controlResult{}, ErrControlSessionClosed
	}
	op := controlOperation{ctx: ctx, fn: fn, res: make(chan controlResult, 1)}
	select {
	case s.ops <- op:
	case <-ctx.Done():
		return controlResult{}, ctx.Err()
	case <-s.ctx.Done():
		return controlResult{}, context.Cause(s.ctx)
	}
	select {
	case result := <-op.res:
		return result, nil
	case <-ctx.Done():
		return controlResult{}, ctx.Err()
	case <-s.ctx.Done():
		return controlResult{}, context.Cause(s.ctx)
	}
}

func leadershipKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("stella-ingress:" + name))
	return int64(h.Sum64())
}

// RunLeader waits for a session advisory lock and invokes run only while this
// physical connection owns it. run must stop and join all listeners before it
// returns after ctx is canceled; returning early also ends the epoch. A
// connection loss cancels run immediately;
// after reconnect, RunLeader performs a full lock scan and retries.
func (s *ControlSession) RunLeader(ctx context.Context, name string, run func(context.Context)) error {
	if s == nil {
		return ErrControlSessionClosed
	}
	if name == "" || run == nil {
		return errors.New("control-session leadership requires a name and callback")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, callCancel := context.WithCancelCause(ctx)
	s.closeMu.Lock()
	if s.closeStarted {
		s.closeMu.Unlock()
		callCancel(ErrControlSessionClosed)
		return ErrControlSessionClosed
	}
	s.nextLeaderID++
	leaderID := s.nextLeaderID
	s.leaderCancels[leaderID] = callCancel
	s.leaders.Add(1)
	s.closeMu.Unlock()
	defer func() {
		callCancel(context.Canceled)
		s.closeMu.Lock()
		delete(s.leaderCancels, leaderID)
		s.closeMu.Unlock()
		s.leaders.Done()
	}()
	// PostgreSQL session advisory locks are reentrant: two callers sharing this
	// physical connection would both observe pg_try_advisory_lock=true for the
	// same key. Keep a process-local ownership bit around each successful lock
	// so one ControlSession cannot run duplicate listeners for one name.
	releaseName := func() {}
	defer func() { releaseName() }()
	releaseClaim := func() {
		releaseName()
		releaseName = func() {}
	}
	key := leadershipKey(name)
	for callCtx.Err() == nil && s.ctx.Err() == nil {
		var err error
		releaseName, err = s.claimLeaderName(callCtx, name)
		if err != nil {
			if callCtx.Err() != nil {
				return context.Cause(callCtx)
			}
			if s.ctx.Err() != nil {
				return context.Cause(s.ctx)
			}
			return s.normalizeCloseError(err)
		}
		result, err := s.execute(callCtx, func(opCtx context.Context, conn *pgx.Conn) (any, error) {
			var ok bool
			err := conn.QueryRow(opCtx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok)
			return ok, err
		})
		if err != nil {
			releaseClaim()
			if callCtx.Err() != nil {
				return context.Cause(callCtx)
			}
			if s.ctx.Err() != nil {
				return context.Cause(s.ctx)
			}
			return s.normalizeCloseError(err)
		}
		if result.err != nil {
			releaseClaim()
			if result.epoch != nil && result.epoch.Err() != nil {
				continue
			}
			return s.normalizeCloseError(result.err)
		}
		ok, okType := result.value.(bool)
		if !okType {
			releaseClaim()
			return fmt.Errorf("control session leadership returned %T, want bool", result.value)
		}
		if ok {
			leaderCtx, cancel := context.WithCancelCause(callCtx)
			stopOnLoss := context.AfterFunc(result.epoch, func() {
				cancel(context.Cause(result.epoch))
			})
			run(leaderCtx)
			// A callback may return because startup failed or because it owns a
			// finite listener. Treat that as a completed leadership epoch instead
			// of waiting forever for a context cancellation that will never come.
			if leaderCtx.Err() == nil {
				cancel(context.Canceled)
			}
			<-leaderCtx.Done()
			stopOnLoss()
			cancel(context.Canceled)

			if result.epoch.Err() == nil && s.ctx.Err() == nil {
				unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
				unlockResult, unlockErr := s.execute(unlockCtx, func(opCtx context.Context, conn *pgx.Conn) (any, error) {
					var unlocked bool
					err := conn.QueryRow(opCtx, "SELECT pg_advisory_unlock($1)", key).Scan(&unlocked)
					return unlocked, err
				})
				unlockCancel()
				if unlockErr != nil {
					releaseClaim()
					// Close marks the session before canceling callbacks so a new
					// leader cannot enter while the old callback drains. Its physical
					// connection close will release the advisory lock after that
					// drain; report the expected cancellation rather than exposing the
					// internal close gate as a leadership failure.
					return s.normalizeCloseError(unlockErr)
				}
				if unlockResult.err != nil {
					releaseClaim()
					return s.normalizeCloseError(unlockResult.err)
				}
			}
			releaseClaim()
			if result.epoch.Err() != nil && callCtx.Err() == nil && s.ctx.Err() == nil {
				// Connection loss already canceled and drained run. The next
				// iteration waits for a new backend and performs a full scan.
				continue
			}
			if callCtx.Err() != nil {
				return context.Cause(callCtx)
			}
			if s.isClosing() {
				return context.Canceled
			}
			return context.Cause(leaderCtx)
		}
		releaseClaim()

		timer := time.NewTimer(time.Second)
		select {
		case <-callCtx.Done():
			timer.Stop()
		case <-s.ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return errors.Join(context.Cause(callCtx), context.Cause(s.ctx))
}

func (s *ControlSession) normalizeCloseError(err error) error {
	if errors.Is(err, ErrControlSessionClosed) && s.isClosing() {
		return context.Canceled
	}
	return err
}

func (s *ControlSession) isClosing() bool {
	if s == nil {
		return true
	}
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeStarted
}

// claimLeaderName serializes same-name leadership attempts that share this
// physical connection. The channel is replaced whenever a name is released,
// which lets waiters observe progress without polling while still honoring
// their caller context.
func (s *ControlSession) claimLeaderName(ctx context.Context, name string) (func(), error) {
	for {
		s.closeMu.Lock()
		if s.closeStarted {
			s.closeMu.Unlock()
			return nil, ErrControlSessionClosed
		}
		if _, exists := s.leaderNames[name]; !exists {
			s.leaderNames[name] = struct{}{}
			s.closeMu.Unlock()
			return func() { s.releaseLeaderName(name) }, nil
		}
		changed := s.nameChanges
		s.closeMu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-s.ctx.Done():
			return nil, context.Cause(s.ctx)
		}
	}
}

func (s *ControlSession) releaseLeaderName(name string) {
	s.closeMu.Lock()
	if _, exists := s.leaderNames[name]; exists {
		delete(s.leaderNames, name)
		close(s.nameChanges)
		s.nameChanges = make(chan struct{})
	}
	s.closeMu.Unlock()
}

// Close first cancels and joins every leader callback, then closes the physical
// connection loop. This ordering lets session-scoped advisory locks release
// only after their listeners stopped. A timeout affects only the caller's wait;
// the close continues in the background so a later caller can join it.
func (s *ControlSession) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.closeMu.Lock()
	if !s.closeStarted {
		s.closeStarted = true
		cancels := make([]context.CancelCauseFunc, 0, len(s.leaderCancels))
		for _, cancel := range s.leaderCancels {
			cancels = append(cancels, cancel)
		}
		go s.closeInternal(cancels)
	}
	done := s.closeDone
	s.closeMu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
}

func (s *ControlSession) closeInternal(cancels []context.CancelCauseFunc) {
	// Keep the physical connection alive while callbacks perform their listener
	// cleanup and RunLeader releases its session advisory locks.
	for _, cancel := range cancels {
		cancel(context.Canceled)
	}
	s.leaders.Wait()
	s.cancel(context.Canceled)
	<-s.done
	if s.parentStop != nil {
		s.parentStop()
	}
	s.closeMu.Lock()
	close(s.closeDone)
	s.closeMu.Unlock()
}
