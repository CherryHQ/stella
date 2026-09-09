package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/agentrun"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func isNotStarted(err error) bool {
	var marker *pkgsandbox.NotStartedError
	return errors.As(err, &marker)
}

// uncertainFence is the fail-closed fence shared by generation and auxiliary
// preparation capabilities: the first uncertain outcome wins and is never
// cleared.
type uncertainFence struct {
	mu  sync.RWMutex
	err error
}

func (f *uncertainFence) markUncertain(err error) {
	if err == nil {
		return
	}
	f.mu.Lock()
	if f.err == nil {
		f.err = err
	}
	f.mu.Unlock()
}

func (f *uncertainFence) uncertain() error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.err
}

// mergeEnvOverlay overlays incremental credential updates onto a base
// environment without mutating either input.
func mergeEnvOverlay(base, updates map[string]string) map[string]string {
	if len(updates) == 0 {
		return base
	}
	merged := maps.Clone(base)
	if merged == nil {
		merged = make(map[string]string, len(updates))
	}
	maps.Copy(merged, updates)
	return merged
}

// generationSession is the retained runner-facing session. Every recreation
// obtains a new durable generation through GenerationStore and returns a
// guarded wrapper.
type generationSession struct {
	store *GenerationStore
	spec  GenerationSpec

	// recreateMu serializes recreation with itself and with RefreshEnv and
	// ReleaseBorrow: concurrent borrowers join the new generation instead of
	// racing duplicate creates, a racing refresh lands after its overlay
	// reset, and the final borrow is the one released.
	recreateMu sync.Mutex
	mu         sync.Mutex
	current    *generationRawSession
	envUpdates map[string]string
	// borrow is the runner's lease on the current generation. The zero key
	// expresses an absent borrow; a real key always has a nonempty SessionID.
	borrow generationKey
	// blocked is a per-borrow diagnostic. The authoritative fence is on the
	// shared generationRawSession, so every borrower observes the same error.
	blocked   error
	closed    bool
	ownerDone bool
	ownerErr  error
}

// recreate replaces a locally dead current generation with a fresh one
// through the durable store.
func (s *generationSession) recreate(ctx context.Context) (*generationRawSession, error) {
	s.recreateMu.Lock()
	defer s.recreateMu.Unlock()
	// Another borrower may have completed the recreation while this one waited.
	s.mu.Lock()
	current, closed := s.current, s.closed
	s.mu.Unlock()
	if closed {
		return nil, errors.New("sandbox: session is permanently closed")
	}
	if current != nil {
		if err := current.uncertain(); err != nil {
			return nil, s.blockedError(err)
		}
		if current.Alive() {
			return current, nil
		}
	}
	raw, err := s.store.openRaw(ctx, s.spec, s)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *generationSession) blockedError(err error) error {
	s.mu.Lock()
	if s.blocked == nil {
		s.blocked = err
	}
	blocked := s.blocked
	s.mu.Unlock()
	// The stored provider cause is diagnostic only. Wrapping it would make a
	// later rejection look like a replayable provider failure.
	//nolint:errorlint // preserve the diagnostic-only provider cause
	return fmt.Errorf("sandbox: generation is blocked after an uncertain operation: %w: %v", ErrGenerationUnknown, blocked)
}

// bind performs the one ownership transition: it installs a generation as the
// current owner capability together with its borrow, and resets the credential
// overlay (a freshly allocated owner has none to reset). Releasing a replaced
// distinct borrow happens after unlocking, outside store.mu.
func (s *generationSession) bind(raw *generationRawSession, key generationKey) {
	s.mu.Lock()
	previous := s.borrow
	s.current = raw
	s.borrow = key
	s.envUpdates = nil
	s.mu.Unlock()
	if previous != (generationKey{}) && previous != key {
		s.store.release(previous)
	}
}

// ReleaseBorrow drops the runner's lease while retaining the owner-held raw
// generation. Runtime idle reaping uses this path; it must never fence compute.
// It serializes with recreation so a borrow installed by an in-flight
// recreation is the one released instead of leaking a retention slot.
func (s *generationSession) ReleaseBorrow() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.recreateMu.Lock()
	defer s.recreateMu.Unlock()
	s.mu.Lock()
	key := s.borrow
	s.mu.Unlock()
	s.store.release(key)
	return nil
}

// selected returns the guarded current generation, recreating it through the
// durable store when the previous one is locally dead. Stale or uncertain
// operations fail here, before any effect.
func (s *generationSession) selected(ctx context.Context) (*generationRawSession, error) {
	s.mu.Lock()
	current, closed := s.current, s.closed
	s.mu.Unlock()
	if closed {
		return nil, errors.New("sandbox: session is permanently closed")
	}
	if current == nil {
		return nil, ErrGenerationNotFound
	}
	if err := current.uncertain(); err != nil {
		return nil, s.blockedError(err)
	}
	if current.Alive() {
		if err := current.guard(ctx); err != nil {
			return nil, err
		}
		return current, nil
	}
	raw, err := s.recreate(ctx)
	if err != nil {
		return nil, err
	}
	if err := raw.guard(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *generationSession) Policy() pkgsandbox.Policy {
	s.mu.Lock()
	current, closed, updates := s.current, s.closed, maps.Clone(s.envUpdates)
	s.mu.Unlock()
	if closed || current == nil {
		return pkgsandbox.Policy{}
	}
	policy := current.Policy()
	policy.Env = mergeEnvOverlay(policy.Env, updates)
	return policy
}

func (s *generationSession) WorkingDir() string {
	s.mu.Lock()
	current, closed := s.current, s.closed
	s.mu.Unlock()
	if closed || current == nil {
		return ""
	}
	return current.WorkingDir()
}

func (s *generationSession) Alive() bool {
	s.mu.Lock()
	current, closed := s.current, s.closed
	s.mu.Unlock()
	return !closed && current != nil && current.Alive()
}

func (s *generationSession) Done() <-chan struct{} {
	s.mu.Lock()
	current := s.current
	s.mu.Unlock()
	if current == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return current.Done()
}

func (s *generationSession) Close() error {
	return s.ReleaseBorrow()
}

// CloseOwner is the terminal lifecycle path used by explicit Session/runtime
// shutdown. It fences and closes the owner-held provider resource. Ordinary
// Runner.Close calls Close, which only releases that runner's borrow.
func (s *generationSession) CloseOwner() error {
	s.mu.Lock()
	if s.ownerDone {
		err := s.ownerErr
		s.mu.Unlock()
		return err
	}
	s.closed = true
	s.mu.Unlock()
	// Serialization keeps a concurrent recreation from returning a fresh
	// generation after the owner has already closed.
	s.recreateMu.Lock()
	defer s.recreateMu.Unlock()
	s.mu.Lock()
	current := s.current
	s.mu.Unlock()
	var err error
	if current != nil {
		err = current.Close()
	}
	s.mu.Lock()
	s.ownerErr = err
	s.ownerDone = err == nil
	s.mu.Unlock()
	return err
}

func (s *generationSession) Exec(ctx context.Context, command string, opts pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	raw, err := s.selected(ctx)
	if err != nil {
		return pkgsandbox.ExecResult{}, err
	}
	return raw.Exec(ctx, command, opts)
}

func (s *generationSession) StartProcess(ctx context.Context, req pkgsandbox.ProcessRequest) (pkgsandbox.ProcessHandle, error) {
	raw, err := s.selected(ctx)
	if err != nil {
		return nil, err
	}
	return raw.StartProcess(ctx, req)
}

func (s *generationSession) Files() pkgsandbox.FileAccess {
	raw, err := s.selected(context.Background())
	if err != nil {
		return generationErrorFiles{err: err}
	}
	return raw.Files()
}

// SelectFileView binds Policy, WorkingDir and FileAccess to one generation.
// It selects once here instead of exposing a dynamic Files accessor that
// could pick a different generation per file operation.
func (s *generationSession) SelectFileView(ctx context.Context) (pkgsandbox.FileView, error) {
	raw, err := s.selected(ctx)
	if err != nil {
		return pkgsandbox.FileView{}, err
	}
	return raw.fileView(ctx)
}

// RefreshEnv overlays credential rotations for subsequent Policy reads. It
// serializes with recreation so a refresh racing a recreate lands after the
// new generation's overlay reset instead of being discarded by it.
func (s *generationSession) RefreshEnv(updates map[string]string) {
	if len(updates) == 0 {
		return
	}
	s.recreateMu.Lock()
	defer s.recreateMu.Unlock()
	s.mu.Lock()
	s.envUpdates = mergeEnvOverlay(s.envUpdates, updates)
	s.mu.Unlock()
}

func (s *generationSession) TurnDeadline() (time.Time, bool) {
	s.mu.Lock()
	current := s.current
	s.mu.Unlock()
	if current == nil {
		return time.Time{}, false
	}
	return current.TurnDeadline()
}

func (s *generationSession) Sync() error {
	raw, err := s.selected(context.Background())
	if err != nil {
		return err
	}
	return raw.Sync()
}

func (s *generationSession) RenderEnv(ctx context.Context, env map[string]string) (map[string]string, error) {
	raw, err := s.selected(ctx)
	if err != nil {
		return nil, err
	}
	return raw.RenderEnv(ctx, env)
}

func (s *generationSession) PreparePluginBinaries(ctx context.Context, specs []pkgplugins.PluginBinarySpec) (pkgplugins.PluginPreparationResult, []string, error) {
	raw, err := s.selected(ctx)
	if err != nil {
		return pkgplugins.PluginPreparationResult{}, nil, err
	}
	return raw.PreparePluginBinaries(ctx, specs)
}

func (s *generationSession) PluginPreparationResult() pkgplugins.PluginPreparationResult {
	raw, err := s.selected(context.Background())
	if err != nil {
		return pkgplugins.PluginPreparationResult{}
	}
	return raw.PluginPreparationResult()
}

func (s *generationSession) ResourceIdentity(ctx context.Context) (pkgsandbox.ResourceIdentity, error) {
	raw, err := s.selected(ctx)
	if err != nil {
		return pkgsandbox.ResourceIdentity{}, err
	}
	return raw.ResourceIdentity(ctx)
}

// generationRawSession guards the complete raw provider capability surface.
// It is the only capability handed out by selected(), so callers cannot reach
// a raw backend without passing this fence.
type generationRawSession struct {
	store        *GenerationStore
	key          generationKey
	row          GenerationRecord
	raw          pkgsandbox.Session
	identity     pkgsandbox.ResourceIdentity
	closeMu      sync.Mutex
	closed       bool
	closeErr     error
	resourceGone bool
	cleanupDone  bool
	uncertainFence
}

func (s *generationRawSession) localAlive() bool {
	s.closeMu.Lock()
	closed := s.closed
	s.closeMu.Unlock()
	return !closed && s.raw != nil && s.raw.Alive()
}

func (s *generationRawSession) guard(ctx context.Context) error {
	if s.store == nil {
		return errors.New("sandbox: generation store is unavailable")
	}
	if err := s.uncertain(); err != nil {
		return fmt.Errorf("%w: local resource outcome is uncertain: %w", ErrGenerationUnknown, err)
	}
	if err := agentrun.Check(ctx); err != nil {
		return err
	}
	_, err := s.store.guard(ctx, s.key, s.raw)
	return err
}

func (s *generationRawSession) operationError(detail string, err error) {
	if err == nil || isNotStarted(err) {
		return
	}
	// Set the local fence before attempting the durable transition. If the
	// database write fails, every borrow of this raw generation still fails
	// closed, and a future Open can reconcile it once the DB is available.
	s.markUncertain(err)
	s.store.markUnknown(context.Background(), s.row, detail)
}

func (s *generationRawSession) Policy() pkgsandbox.Policy {
	if err := s.guard(context.Background()); err != nil {
		return pkgsandbox.Policy{}
	}
	return s.raw.Policy()
}

func (s *generationRawSession) WorkingDir() string {
	if err := s.guard(context.Background()); err != nil {
		return ""
	}
	return s.raw.WorkingDir()
}

func (s *generationRawSession) Alive() bool {
	return s.localAlive() && s.guard(context.Background()) == nil
}

func (s *generationRawSession) Done() <-chan struct{} { return s.raw.Done() }

func (s *generationRawSession) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		if s.cleanupDone {
			return s.closeErr
		}
	} else {
		// Explicit close must fence before touching the provider. The CAS is
		// deliberately separate from destruction so a failed close leaves unknown.
		fenced, err := s.store.FenceGeneration(context.Background(), s.row, "closing sandbox generation")
		if err != nil {
			return err
		}
		s.row = fenced
		s.closed = true
	}
	// Preparation sessions can own a separate provider resource or private
	// staging root. Drain them before touching the parent resource, because a
	// parent close may remove the only namespace in which they are addressable.
	if err := s.store.closeAuxiliaries(context.Background(), s.row); err != nil {
		s.closeErr = err
		return err
	}

	// Durable-controller backends must prove and record provider absence before
	// raw.Close, because raw.Close may delete the only addressable object (for
	// example a Kubernetes Pod) before the controller can probe it.
	controller, controllerErr := s.store.controller(context.Background(), s.row.Backend)
	if controllerErr != nil {
		s.closeErr = controllerErr
		s.store.markUnknown(context.Background(), s.row, fmt.Sprintf("construct resource controller before close: %v", controllerErr))
		return controllerErr
	}
	if controller != nil && (s.row.Resource.Authority == "" || s.row.Resource.Ref == "") {
		s.closeErr = ErrGenerationUnknown
		s.store.markUnknown(context.Background(), s.row, "resource identity is unavailable; close cannot prove absence")
		return s.closeErr
	}
	if controller != nil {
		if !s.resourceGone {
			if err := s.store.CloseGeneration(context.Background(), s.row, "closed by owner"); err != nil {
				s.closeErr = err
				return err
			}
			s.resourceGone = true
		}
		if err := s.raw.Close(); err != nil {
			s.closeErr = err
			return err
		}
		s.closeErr = nil
		s.cleanupDone = true
		return nil
	}

	// Controllerless resources may still expose a local proof while this raw
	// session is alive. Clean up first, then ask that optional capability; a
	// restarted executor cannot reconstruct this proof and must remain unknown.
	if err := s.raw.Close(); err != nil {
		s.closeErr = err
		s.store.markUnknown(context.Background(), s.row, fmt.Sprintf("close generation: %v", err))
		return err
	}
	observer, ok := s.raw.(pkgsandbox.ResourceObservationProvider)
	if !ok {
		s.closeErr = ErrGenerationNoControl
		s.store.markUnknown(context.Background(), s.row, "controllerless backend cannot prove resource absence after close")
		return s.closeErr
	}
	observation, err := observer.ObserveResource(context.Background())
	if err != nil {
		s.closeErr = err
		s.store.markUnknown(context.Background(), s.row, fmt.Sprintf("observe resource after close: %v", err))
		return err
	}
	if observation.State != pkgsandbox.ResourceStateAbsent {
		detail := observation.Detail
		if detail == "" {
			detail = fmt.Sprintf("close did not prove resource absence: state=%s", observation.State)
		}
		s.closeErr = ErrGenerationUnknown
		s.store.markUnknown(context.Background(), s.row, detail)
		return s.closeErr
	}
	if err := s.store.markObservedAbsent(context.Background(), s.row, observation.Detail); err != nil {
		// The provider is locally cleaned up, but the durable CAS may still be
		// retryable after a transient database failure. Keep cleanupDone false so
		// a later owner-close retries the state transition.
		s.closeErr = err
		return err
	}
	s.closeErr = nil
	s.cleanupDone = true
	return nil
}

func (s *generationRawSession) Exec(ctx context.Context, command string, opts pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	if err := s.guard(ctx); err != nil {
		return pkgsandbox.ExecResult{}, err
	}
	result, err := s.raw.Exec(ctx, command, opts)
	if err != nil {
		s.operationError(fmt.Sprintf("exec generation: %v", err), err)
	}
	return result, err
}

func (s *generationRawSession) StartProcess(ctx context.Context, req pkgsandbox.ProcessRequest) (pkgsandbox.ProcessHandle, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	handle, err := s.raw.StartProcess(ctx, req)
	if err != nil {
		s.operationError(fmt.Sprintf("start process: %v", err), err)
		return nil, err
	}
	if handle == nil {
		err := errors.New("sandbox: backend returned nil process handle")
		s.operationError(err.Error(), err)
		return nil, err
	}
	return &generationProcessHandle{owner: s, inner: handle, ctx: ctx}, nil
}

func (s *generationRawSession) Files() pkgsandbox.FileAccess {
	return &generationFileAccess{owner: s, inner: s.raw.Files(), ctx: context.Background()}
}

func (s *generationRawSession) fileView(ctx context.Context) (pkgsandbox.FileView, error) {
	if err := s.guard(ctx); err != nil {
		return pkgsandbox.FileView{}, err
	}
	return pkgsandbox.FileView{Policy: s.raw.Policy(), WorkingDir: s.raw.WorkingDir(), Files: &generationFileAccess{owner: s, inner: s.raw.Files(), ctx: ctx}}, nil
}

func (s *generationRawSession) ResourceIdentity(ctx context.Context) (pkgsandbox.ResourceIdentity, error) {
	if err := s.guard(ctx); err != nil {
		return pkgsandbox.ResourceIdentity{}, err
	}
	return s.identity, nil
}

func (s *generationRawSession) Sync() error {
	if err := s.guard(context.Background()); err != nil {
		return err
	}
	if syncer, ok := s.raw.(interface{ Sync() error }); ok {
		return syncer.Sync()
	}
	return nil
}

func (s *generationRawSession) RenderEnv(ctx context.Context, env map[string]string) (map[string]string, error) {
	if err := s.guard(ctx); err != nil {
		return nil, err
	}
	if renderer, ok := s.raw.(pkgsandbox.EnvRenderer); ok {
		return renderer.RenderEnv(ctx, env)
	}
	return pkgsandbox.RenderEnv(ctx, s.raw, env)
}

func (s *generationRawSession) TurnDeadline() (time.Time, bool) {
	if err := s.guard(context.Background()); err != nil {
		return time.Time{}, false
	}
	if timed, ok := s.raw.(pkgsandbox.TurnDeadlineProvider); ok {
		return timed.TurnDeadline()
	}
	return time.Time{}, false
}

func (s *generationRawSession) PreparePluginBinaries(ctx context.Context, specs []pkgplugins.PluginBinarySpec) (pkgplugins.PluginPreparationResult, []string, error) {
	if err := s.guard(ctx); err != nil {
		return pkgplugins.PluginPreparationResult{}, nil, err
	}
	preparer, ok := s.raw.(interface {
		PreparePluginBinaries(context.Context, []pkgplugins.PluginBinarySpec) (pkgplugins.PluginPreparationResult, []string, error)
	})
	if !ok {
		return pkgplugins.PluginPreparationResult{}, nil, errors.New("sandbox: session does not support plugin binary preparation")
	}
	return preparer.PreparePluginBinaries(ctx, specs)
}

func (s *generationRawSession) PluginPreparationResult() pkgplugins.PluginPreparationResult {
	if s.guard(context.Background()) != nil {
		return pkgplugins.PluginPreparationResult{}
	}
	if provider, ok := s.raw.(interface {
		PluginPreparationResult() pkgplugins.PluginPreparationResult
	}); ok {
		return provider.PluginPreparationResult()
	}
	return pkgplugins.PluginPreparationResult{}
}

type generationProcessHandle struct {
	owner *generationRawSession
	inner pkgsandbox.ProcessHandle
	ctx   context.Context
}

func (h *generationProcessHandle) PID() int { return h.inner.PID() }
func (h *generationProcessHandle) Stdin() io.WriteCloser {
	return &generationStdin{owner: h.owner, inner: h.inner.Stdin(), ctx: h.ctx}
}
func (h *generationProcessHandle) Stdout() io.ReadCloser { return h.inner.Stdout() }
func (h *generationProcessHandle) Stderr() io.ReadCloser { return h.inner.Stderr() }
func (h *generationProcessHandle) Wait(ctx context.Context) (pkgsandbox.ExecResult, error) {
	if err := h.owner.guard(ctx); err != nil {
		return pkgsandbox.ExecResult{}, err
	}
	result, err := h.inner.Wait(ctx)
	if err != nil {
		h.owner.operationError(fmt.Sprintf("wait process: %v", err), err)
	}
	return result, err
}

func (h *generationProcessHandle) Close() error {
	if err := h.owner.guard(context.Background()); err != nil {
		return err
	}
	return h.inner.Close()
}

type generationStdin struct {
	owner *generationRawSession
	inner io.WriteCloser
	ctx   context.Context
}

func (w *generationStdin) Write(p []byte) (int, error) {
	if err := w.owner.guard(w.ctx); err != nil {
		return 0, err
	}
	if w.inner == nil {
		return 0, io.ErrClosedPipe
	}
	return w.inner.Write(p)
}

func (w *generationStdin) Close() error {
	if w.inner == nil {
		return nil
	}
	return w.inner.Close()
}

type generationFileAccess struct {
	owner *generationRawSession
	inner pkgsandbox.FileAccess
	ctx   context.Context
}

func (f *generationFileAccess) check() error {
	ctx := f.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return f.owner.guard(ctx)
}

func (f *generationFileAccess) ReadFile(path string) ([]byte, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	return f.inner.ReadFile(path)
}

func (f *generationFileAccess) ReadDir(path string) ([]pkgsandbox.DirEntry, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	return f.inner.ReadDir(path)
}

func (f *generationFileAccess) Stat(path string) (pkgsandbox.FileInfo, error) {
	if err := f.check(); err != nil {
		return pkgsandbox.FileInfo{}, err
	}
	return f.inner.Stat(path)
}

func (f *generationFileAccess) WriteFile(path string, content []byte, mode fs.FileMode) error {
	if err := f.check(); err != nil {
		return err
	}
	return f.inner.WriteFile(path, content, mode)
}

func (f *generationFileAccess) ProjectFiles(path string, files []pkgsandbox.ProjectedFile) error {
	if err := f.check(); err != nil {
		return err
	}
	return f.inner.ProjectFiles(path, files)
}

func (f *generationFileAccess) ProjectTempFiles(path string, files []pkgsandbox.ProjectedFile) (string, error) {
	if err := f.check(); err != nil {
		return "", err
	}
	return f.inner.ProjectTempFiles(path, files)
}

type generationErrorFiles struct{ err error }

func (f generationErrorFiles) ReadFile(string) ([]byte, error)               { return nil, f.err }
func (f generationErrorFiles) ReadDir(string) ([]pkgsandbox.DirEntry, error) { return nil, f.err }
func (f generationErrorFiles) Stat(string) (pkgsandbox.FileInfo, error) {
	return pkgsandbox.FileInfo{}, f.err
}
func (f generationErrorFiles) WriteFile(string, []byte, fs.FileMode) error           { return f.err }
func (f generationErrorFiles) ProjectFiles(string, []pkgsandbox.ProjectedFile) error { return f.err }
func (f generationErrorFiles) ProjectTempFiles(string, []pkgsandbox.ProjectedFile) (string, error) {
	return "", f.err
}
