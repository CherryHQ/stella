package agentrun

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type SandboxLease struct {
	store           *Store
	guard           Guard
	generation      int64
	resourceBackend string
	resourceID      string
}

// ReserveSandbox fences compute independently from Workspace bytes. backend is
// persisted with a deterministic provider resource identity before creation,
// so recovery can clean up even if the executor dies after an outcome-unknown
// create. A nil lease means the caller is outside an AgentRun (primarily focused
// tests). The optional form preserves process-only test callers.
func ReserveSandbox(ctx context.Context, sessionID string, backendName ...string) (*SandboxLease, error) {
	value, ok := ctx.Value(guardKey{}).(guardedContext)
	if !ok || value.store == nil {
		return nil, nil
	}
	if sessionID == "" || sessionID != value.SessionID {
		return nil, errors.New("SessionSandbox session does not match AgentRun owner")
	}
	backend := "process"
	if len(backendName) > 0 && backendName[0] != "" {
		backend = backendName[0]
	}
	resourceID := pkgsandbox.NewSessionID()
	row, err := WriteTxValue(ctx, value.store.db, func(q *sqlc.Queries) (sqlc.AgentSessionSandbox, error) {
		return q.CreateSessionSandboxGeneration(ctx, sqlc.CreateSessionSandboxGenerationParams{
			SessionID: sessionID, ExecutorBootID: pgtype.Text{String: value.ExecutorBootID, Valid: true},
			RunID: pgtype.Text{String: value.RunID, Valid: true}, ResourceBackend: backend, ResourceID: resourceID,
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("previous SessionSandbox generation is not proven destroyed")
	}
	if err != nil {
		return nil, err
	}
	return &SandboxLease{
		store: value.store, guard: value.Guard, generation: row.Generation,
		resourceBackend: backend, resourceID: resourceID,
	}, nil
}

func (l *SandboxLease) ResourceID() string {
	if l == nil {
		return ""
	}
	return l.resourceID
}

func (l *SandboxLease) Activate(ctx context.Context, inner pkgsandbox.Session) (pkgsandbox.Session, error) {
	if l == nil {
		return inner, nil
	}
	// The caller may be completing setup on a provider-owned context rather than
	// the Run context. Bind the immutable owner here so activation and ownership
	// validation still commit in one transaction.
	guardedCtx := withLeaseGuard(ctx, l.guard, l.store)
	rows, err := WriteTxValue(guardedCtx, l.store.db, func(q *sqlc.Queries) (int64, error) {
		return q.ActivateSessionSandboxGeneration(guardedCtx, sqlc.ActivateSessionSandboxGenerationParams{
			SessionID: l.guard.SessionID, Generation: l.generation,
			ExecutorBootID: pgtype.Text{String: l.guard.ExecutorBootID, Valid: true},
			RunID:          pgtype.Text{String: l.guard.RunID, Valid: true},
		})
	})
	if err != nil || rows != 1 {
		activateErr := err
		if activateErr == nil {
			activateErr = ErrLeaseLost
		}
		// The resource was created but never handed to a fenced caller. Fence its
		// durable generation before provider cleanup starts, then mark destroyed
		// only after Close proves it absent. Use a fresh cleanup context because
		// lease loss commonly cancels ctx.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		alreadyDestroyed, fenceErr := l.fence(cleanupCtx)
		if fenceErr != nil {
			return nil, errors.Join(activateErr, fenceErr)
		}
		if closeErr := inner.Close(); closeErr != nil {
			return nil, errors.Join(activateErr, closeErr)
		}
		if alreadyDestroyed {
			return nil, activateErr
		}
		return nil, errors.Join(activateErr, l.destroy(cleanupCtx))
	}
	return &fencedSandbox{inner: inner, lease: l}, nil
}

// fence transitions this exact generation out of every executable state before
// provider cleanup. The boolean reports that another cleanup already proved it
// destroyed; callers must still clean any resource handle they just obtained.
func (l *SandboxLease) fence(ctx context.Context) (bool, error) {
	rows, err := l.store.q.FenceSessionSandboxGeneration(ctx, sqlc.FenceSessionSandboxGenerationParams{
		SessionID: l.guard.SessionID, Generation: l.generation,
		ExecutorBootID: pgtype.Text{String: l.guard.ExecutorBootID, Valid: true},
		RunID:          pgtype.Text{String: l.guard.RunID, Valid: true},
	})
	if err != nil {
		return false, err
	}
	if rows == 1 {
		return false, nil
	}
	row, getErr := l.store.q.GetSessionSandbox(ctx, l.guard.SessionID)
	if getErr != nil || row.Generation != l.generation || (row.State != "fenced" && row.State != "destroyed") {
		return false, errors.Join(ErrLeaseLost, getErr)
	}
	return row.State == "destroyed", nil
}

func (l *SandboxLease) destroy(ctx context.Context) error {
	rows, err := l.store.q.DestroySessionSandboxGeneration(ctx, sqlc.DestroySessionSandboxGenerationParams{
		SessionID: l.guard.SessionID, Generation: l.generation,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (l *SandboxLease) abandon(ctx context.Context) error {
	rows, err := l.store.q.FenceSessionSandboxGeneration(ctx, sqlc.FenceSessionSandboxGenerationParams{
		SessionID: l.guard.SessionID, Generation: l.generation,
		ExecutorBootID: pgtype.Text{String: l.guard.ExecutorBootID, Valid: true},
		RunID:          pgtype.Text{String: l.guard.RunID, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		row, getErr := l.store.q.GetSessionSandbox(ctx, l.guard.SessionID)
		if getErr != nil || row.Generation != l.generation || (row.State != "fenced" && row.State != "destroyed") {
			return errors.Join(ErrLeaseLost, getErr)
		}
		if row.State == "destroyed" {
			return nil
		}
	}
	rows, err = l.store.q.DestroySessionSandboxGeneration(ctx, sqlc.DestroySessionSandboxGenerationParams{
		SessionID: l.guard.SessionID, Generation: l.generation,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (l *SandboxLease) Abandon(ctx context.Context) error {
	if l == nil {
		return nil
	}
	return l.abandon(ctx)
}

// CleanupCreationFailure resolves the outcome-unknown provider-create window
// from the identity persisted before creation. Replacement remains blocked if
// provider cleanup cannot prove the named resource absent.
func (l *SandboxLease) CleanupCreationFailure(ctx context.Context) error {
	if l == nil {
		return nil
	}
	rows, err := l.store.q.FenceSessionSandboxGeneration(ctx, sqlc.FenceSessionSandboxGenerationParams{
		SessionID: l.guard.SessionID, Generation: l.generation,
		ExecutorBootID: pgtype.Text{String: l.guard.ExecutorBootID, Valid: true},
		RunID:          pgtype.Text{String: l.guard.RunID, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		row, getErr := l.store.q.GetSessionSandbox(ctx, l.guard.SessionID)
		if getErr != nil || row.Generation != l.generation || (row.State != "fenced" && row.State != "destroyed") {
			return errors.Join(ErrLeaseLost, getErr)
		}
		if row.State == "destroyed" {
			return nil
		}
	}
	if l.store.sandboxCleaner == nil {
		return errors.New("SessionSandbox cleanup is not configured")
	}
	if err := l.store.sandboxCleaner(ctx, l.resourceBackend, l.resourceID); err != nil {
		return err
	}
	rows, err = l.store.q.DestroySessionSandboxGeneration(ctx, sqlc.DestroySessionSandboxGenerationParams{
		SessionID: l.guard.SessionID, Generation: l.generation,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (l *SandboxLease) validate(ctx context.Context) error {
	_, err := l.store.q.ValidateSessionSandboxGeneration(ctx, sqlc.ValidateSessionSandboxGenerationParams{
		SessionID: l.guard.SessionID, Generation: l.generation,
		ExecutorBootID: pgtype.Text{String: l.guard.ExecutorBootID, Valid: true},
		RunID:          pgtype.Text{String: l.guard.RunID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	return err
}

func (l *SandboxLease) validateOperation(ctx context.Context) error {
	if err := Check(withLeaseGuard(ctx, l.guard, l.store)); err != nil {
		return err
	}
	return l.validate(ctx)
}

type fencedSandbox struct {
	inner     pkgsandbox.Session
	lease     *SandboxLease
	cleanupMu sync.Mutex
}

func (s *fencedSandbox) check() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.lease.validateOperation(ctx)
}

func (s *fencedSandbox) Policy() pkgsandbox.Policy { return s.inner.Policy() }
func (s *fencedSandbox) WorkingDir() string        { return s.inner.WorkingDir() }
func (s *fencedSandbox) Done() <-chan struct{}     { return s.inner.Done() }
func (s *fencedSandbox) Alive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.lease.validateOperation(ctx); err != nil {
		_ = s.closeAfterFence(ctx)
		return false
	}
	if s.inner.Alive() {
		return true
	}
	// A provider watcher can report dead after a liveness/cleanup error. Dead is
	// not proof of absence: run the ordinary fence-first cleanup path and leave
	// the generation fenced if provider Close cannot prove destruction.
	_ = s.closeAfterFence(ctx)
	return false
}

func (s *fencedSandbox) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.closeAfterFence(ctx)
}

// closeAfterFence is the uncertainty boundary for every compute mutation. The
// generation is fenced before provider cleanup starts and is marked destroyed
// only after Close proves the old resource absent. A failed Close deliberately
// leaves the generation fenced, so replacement cannot begin.
func (s *fencedSandbox) closeAfterFence(ctx context.Context) error {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	// Fence first; only mark destroyed after provider Close confirms absence.
	rows, err := s.lease.store.q.FenceSessionSandboxGeneration(ctx, sqlc.FenceSessionSandboxGenerationParams{
		SessionID: s.lease.guard.SessionID, Generation: s.lease.generation,
		ExecutorBootID: pgtype.Text{String: s.lease.guard.ExecutorBootID, Valid: true},
		RunID:          pgtype.Text{String: s.lease.guard.RunID, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		row, getErr := s.lease.store.q.GetSessionSandbox(ctx, s.lease.guard.SessionID)
		if getErr != nil || row.Generation != s.lease.generation || (row.State != "fenced" && row.State != "destroyed") {
			return errors.Join(ErrLeaseLost, getErr)
		}
		if row.State == "destroyed" {
			return nil
		}
	}
	if err := s.inner.Close(); err != nil {
		return err
	}
	rows, err = s.lease.store.q.DestroySessionSandboxGeneration(ctx, sqlc.DestroySessionSandboxGenerationParams{
		SessionID: s.lease.guard.SessionID, Generation: s.lease.generation,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (s *fencedSandbox) uncertain(operationErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return errors.Join(operationErr, s.closeAfterFence(ctx))
}

func (s *fencedSandbox) Exec(ctx context.Context, command string, opts pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	if err := s.lease.validateOperation(ctx); err != nil {
		return pkgsandbox.ExecResult{}, err
	}
	result, err := s.inner.Exec(ctx, command, opts)
	if err != nil {
		return result, s.uncertain(err)
	}
	if err := s.lease.validateOperation(ctx); err != nil {
		return pkgsandbox.ExecResult{}, s.uncertain(err)
	}
	return result, nil
}

func (s *fencedSandbox) StartProcess(ctx context.Context, req pkgsandbox.ProcessRequest) (pkgsandbox.ProcessHandle, error) {
	if err := s.lease.validateOperation(ctx); err != nil {
		return nil, err
	}
	handle, err := s.inner.StartProcess(ctx, req)
	if err != nil {
		return nil, s.uncertain(err)
	}
	if err := s.lease.validateOperation(ctx); err != nil {
		_ = handle.Close()
		return nil, s.uncertain(err)
	}
	return &fencedProcess{inner: handle, sandbox: s}, nil
}

func (s *fencedSandbox) Files() pkgsandbox.FileAccess {
	return fencedFiles{inner: s.inner.Files(), sandbox: s}
}

type fencedFiles struct {
	inner   pkgsandbox.FileAccess
	sandbox *fencedSandbox
}

func (f fencedFiles) check() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.sandbox.lease.validateOperation(ctx)
}

func (f fencedFiles) ReadFile(path string) ([]byte, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	value, err := f.inner.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := f.check(); err != nil {
		return nil, f.sandbox.uncertain(err)
	}
	return value, nil
}

func (f fencedFiles) ReadDir(path string) ([]pkgsandbox.DirEntry, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	value, err := f.inner.ReadDir(path)
	if err != nil {
		return nil, err
	}
	if err := f.check(); err != nil {
		return nil, f.sandbox.uncertain(err)
	}
	return value, nil
}

func (f fencedFiles) Stat(path string) (pkgsandbox.FileInfo, error) {
	if err := f.check(); err != nil {
		return pkgsandbox.FileInfo{}, err
	}
	value, err := f.inner.Stat(path)
	if err != nil {
		return pkgsandbox.FileInfo{}, err
	}
	if err := f.check(); err != nil {
		return pkgsandbox.FileInfo{}, f.sandbox.uncertain(err)
	}
	return value, nil
}

func (f fencedFiles) WriteFile(path string, content []byte, mode fs.FileMode) error {
	if err := f.check(); err != nil {
		return err
	}
	if err := f.inner.WriteFile(path, content, mode); err != nil {
		return f.sandbox.uncertain(err)
	}
	if err := f.check(); err != nil {
		return f.sandbox.uncertain(err)
	}
	return nil
}

func (f fencedFiles) ProjectFiles(path string, files []pkgsandbox.ProjectedFile) error {
	if err := f.check(); err != nil {
		return err
	}
	if err := f.inner.ProjectFiles(path, files); err != nil {
		return f.sandbox.uncertain(err)
	}
	if err := f.check(); err != nil {
		return f.sandbox.uncertain(err)
	}
	return nil
}

func (f fencedFiles) ProjectTempFiles(path string, files []pkgsandbox.ProjectedFile) (string, error) {
	if err := f.check(); err != nil {
		return "", err
	}
	value, err := f.inner.ProjectTempFiles(path, files)
	if err != nil {
		return "", f.sandbox.uncertain(err)
	}
	if err := f.check(); err != nil {
		return "", f.sandbox.uncertain(err)
	}
	return value, nil
}

type fencedProcess struct {
	inner   pkgsandbox.ProcessHandle
	sandbox *fencedSandbox
}

func (p *fencedProcess) PID() int { return p.inner.PID() }

func (p *fencedProcess) Wait(ctx context.Context) (pkgsandbox.ExecResult, error) {
	if err := p.sandbox.lease.validateOperation(ctx); err != nil {
		return pkgsandbox.ExecResult{}, err
	}
	result, err := p.inner.Wait(ctx)
	if err != nil {
		return result, p.sandbox.uncertain(err)
	}
	if err := p.sandbox.lease.validateOperation(ctx); err != nil {
		return pkgsandbox.ExecResult{}, p.sandbox.uncertain(err)
	}
	return result, nil
}

func (p *fencedProcess) Stdin() io.WriteCloser {
	return &fencedWriteCloser{inner: p.inner.Stdin(), sandbox: p.sandbox}
}

func (p *fencedProcess) Stdout() io.ReadCloser {
	return &fencedReadCloser{inner: p.inner.Stdout(), sandbox: p.sandbox}
}

func (p *fencedProcess) Stderr() io.ReadCloser {
	return &fencedReadCloser{inner: p.inner.Stderr(), sandbox: p.sandbox}
}

func (p *fencedProcess) Close() error {
	if err := p.sandbox.check(); err != nil {
		return err
	}
	if err := p.inner.Close(); err != nil {
		return p.sandbox.uncertain(err)
	}
	if err := p.sandbox.check(); err != nil {
		return p.sandbox.uncertain(err)
	}
	return nil
}

type fencedReadCloser struct {
	inner   io.ReadCloser
	sandbox *fencedSandbox
}

func (r *fencedReadCloser) Read(buf []byte) (int, error) {
	if err := r.sandbox.check(); err != nil {
		return 0, err
	}
	n, err := r.inner.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, r.sandbox.uncertain(err)
	}
	if checkErr := r.sandbox.check(); checkErr != nil {
		return 0, r.sandbox.uncertain(checkErr)
	}
	return n, err
}

func (r *fencedReadCloser) Close() error {
	if err := r.sandbox.check(); err != nil {
		return err
	}
	if err := r.inner.Close(); err != nil {
		return r.sandbox.uncertain(err)
	}
	if err := r.sandbox.check(); err != nil {
		return r.sandbox.uncertain(err)
	}
	return nil
}

type fencedWriteCloser struct {
	inner   io.WriteCloser
	sandbox *fencedSandbox
}

func (w *fencedWriteCloser) Write(buf []byte) (int, error) {
	if err := w.sandbox.check(); err != nil {
		return 0, err
	}
	n, err := w.inner.Write(buf)
	if err != nil {
		return n, w.sandbox.uncertain(err)
	}
	if err := w.sandbox.check(); err != nil {
		return 0, w.sandbox.uncertain(err)
	}
	return n, nil
}

func (w *fencedWriteCloser) Close() error {
	if err := w.sandbox.check(); err != nil {
		return err
	}
	if err := w.inner.Close(); err != nil {
		return w.sandbox.uncertain(err)
	}
	if err := w.sandbox.check(); err != nil {
		return w.sandbox.uncertain(err)
	}
	return nil
}
