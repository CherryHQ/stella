package home

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

type RootScope uint8

const (
	_ RootScope = iota
	RootAgentWorkspace
	RootPrincipalData
	RootSystemSkills
	RootSystemAgentSkills
	RootUserSkills
	RootUserAgentSkills
)

type RootAccess uint8

const (
	RootReadOnly RootAccess = iota
	RootReadWrite
)

var (
	ErrReadLimit      = errors.New("home: read limit exceeded")
	ErrUploadLimit    = errors.New("home: upload limit exceeded")
	ErrListLimit      = errors.New("home: list limit exceeded")
	ErrReadOnly       = errors.New("home: root is read-only")
	ErrOutcomeUnknown = errors.New("home: mutation outcome unknown")
)

func IsOutcomeUnknown(err error) bool { return errors.Is(err, ErrOutcomeUnknown) }

type (
	ListOptions  struct{ Limit int }
	ReadOptions  struct{ MaxBytes int64 }
	WriteOptions struct {
		Mode      fs.FileMode
		Append    bool
		Exclusive bool
		Sync      bool
		MaxBytes  int64
	}
)

type (
	MkdirOptions  struct{ Parents bool }
	RemoveOptions struct{ Recursive bool }
	RenameOptions struct {
		SyncParent bool
		NoReplace  bool
	}
)

type Root struct {
	root   *os.Root
	access RootAccess
	mu     sync.RWMutex
	closed bool
	once   sync.Once
	close  error
	unlock func()
}

// RootOperations is the callback-scoped durable filesystem capability exposed
// to post-authorization consumers. It intentionally contains no physical root
// path or provider/session coordinate.
type RootOperations interface {
	Close() error
	Stat(context.Context, string) (fs.FileInfo, error)
	List(context.Context, string, ListOptions) ([]fs.DirEntry, error)
	Read(context.Context, string, io.Writer, ReadOptions) error
	Write(context.Context, string, io.Reader, WriteOptions) error
	Upload(context.Context, string, io.Reader, WriteOptions) error
	Mkdir(context.Context, string, fs.FileMode, MkdirOptions) error
	Remove(context.Context, string, RemoveOptions) error
	Rename(context.Context, string, string, RenameOptions) error
}

// SkillRootOperations is the POSIX-only extension needed to publish immutable
// Skill revisions beneath an already-authorized typed Home root.
type SkillRootOperations interface {
	RootOperations
	Lstat(context.Context, string) (fs.FileInfo, error)
	Symlink(context.Context, string, string) error
	Readlink(context.Context, string) (string, error)
	SyncDirectory(context.Context, string) error
}

// RootOpener is the narrow capability-minting port for direct durable file
// consumers. WorkspaceManager is the production implementation.
type RootOpener interface {
	OpenRoot(context.Context, WorkspaceRequest, RootScope, RootAccess) (RootOperations, error)
}

// SkillRootOpener mints the complete immutable-publication capability for a
// typed Skill root. Opening an existing root never materializes a missing
// chain, so offline migration dry-runs remain free of Home writes.
type SkillRootOpener interface {
	OpenSkillRoot(context.Context, WorkspaceRequest, RootScope, RootAccess) (SkillRootOperations, error)
	OpenExistingSkillRoot(context.Context, WorkspaceRequest, RootScope) (SkillRootOperations, error)
}

func (m *WorkspaceManager) OpenRoot(ctx context.Context, req WorkspaceRequest, scope RootScope, access RootAccess) (RootOperations, error) {
	return m.openRoot(ctx, req, scope, access, true)
}

func (m *WorkspaceManager) OpenSkillRoot(ctx context.Context, req WorkspaceRequest, scope RootScope, access RootAccess) (SkillRootOperations, error) {
	if !isSkillRootScope(scope) {
		return nil, errors.New("home: Skill root scope is required")
	}
	return m.openRoot(ctx, req, scope, access, true)
}

func (m *WorkspaceManager) OpenExistingSkillRoot(ctx context.Context, req WorkspaceRequest, scope RootScope) (SkillRootOperations, error) {
	if !isSkillRootScope(scope) {
		return nil, errors.New("home: Skill root scope is required")
	}
	return m.openRoot(ctx, req, scope, RootReadOnly, false)
}

func (m *WorkspaceManager) openRoot(ctx context.Context, req WorkspaceRequest, scope RootScope, access RootAccess, create bool) (*Root, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if access != RootReadOnly && access != RootReadWrite {
		return nil, errors.New("home: invalid root access")
	}
	parts, keys, err := m.rootSelection(req, scope)
	if err != nil {
		return nil, err
	}
	unlock, err := m.lock(ctx, keys)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			unlock()
		}
	}()
	if scope == RootAgentWorkspace || scope == RootPrincipalData || scope == RootSystemAgentSkills || scope == RootUserAgentSkills {
		if err = m.agentExists(ctx, req.AgentID); err != nil {
			return nil, err
		}
	}
	switch scope {
	case RootAgentWorkspace, RootPrincipalData:
		kind, id := principal(req)
		if err = m.ownerExists(ctx, kind, id); err != nil {
			return nil, err
		}
	case RootUserSkills, RootUserAgentSkills:
		if err = m.ownerExists(ctx, UserPrincipal, req.UserID); err != nil {
			return nil, err
		}
	}
	if err = checkContext(ctx); err != nil {
		return nil, err
	}
	if create {
		if err = m.ensureChain(parts...); err != nil {
			return nil, err
		}
	}
	if access == RootReadWrite && isSkillRootScope(scope) {
		// Fence every visible component, including components created by an
		// interrupted earlier attempt, before publication can proceed.
		if err = m.syncChain(parts...); err != nil {
			return nil, fmt.Errorf("home: sync typed Skill root ancestry: %w", err)
		}
	}
	or, err := m.openOperationsRoot(parts...)
	if err != nil {
		return nil, err
	}
	ok = true
	return &Root{root: or, access: access, unlock: unlock}, nil
}

func isSkillRootScope(scope RootScope) bool {
	return scope == RootSystemSkills || scope == RootSystemAgentSkills || scope == RootUserSkills || scope == RootUserAgentSkills
}

func principal(req WorkspaceRequest) (PrincipalKind, string) {
	if req.GroupID != "" {
		return GroupPrincipal, req.GroupID
	}
	return UserPrincipal, req.UserID
}

func (m *WorkspaceManager) rootSelection(req WorkspaceRequest, scope RootScope) ([]string, []string, error) {
	switch scope {
	case RootSystemSkills:
		return []string{".agents", "db-skills"}, []string{"system:skills"}, nil
	case RootSystemAgentSkills:
		if err := validID(req.AgentID); err != nil {
			return nil, nil, err
		}
		return []string{"agents", req.AgentID, ".agents", "skills"}, []string{"agent:" + req.AgentID}, nil
	case RootUserSkills:
		if req.GroupID != "" {
			return nil, nil, errors.New("home: user Skill root does not accept group owner")
		}
		if err := validID(req.UserID); err != nil {
			return nil, nil, err
		}
		return []string{"users", req.UserID, ".agents", "skills"}, []string{"user:" + req.UserID}, nil
	case RootUserAgentSkills:
		if req.GroupID != "" {
			return nil, nil, errors.New("home: user-Agent Skill root does not accept group owner")
		}
		if err := validID(req.UserID); err != nil {
			return nil, nil, err
		}
		if err := validID(req.AgentID); err != nil {
			return nil, nil, err
		}
		return []string{"users", req.UserID, ".agents", "agent-skills", req.AgentID}, []string{"agent:" + req.AgentID, "user:" + req.UserID}, nil
	case RootAgentWorkspace, RootPrincipalData:
		if err := validID(req.AgentID); err != nil {
			return nil, nil, err
		}
		kind, id := principal(req)
		if err := validID(id); err != nil {
			return nil, nil, err
		}
		pid := id
		if kind == GroupPrincipal {
			pid = "group-" + id
		}
		parts := []string{"users", pid}
		if scope == RootPrincipalData {
			parts = append(parts, "data")
		} else {
			parts = append(parts, "agents", req.AgentID)
		}
		return parts, []string{"agent:" + req.AgentID, string(kind) + ":" + id}, nil
	default:
		return nil, nil, errors.New("home: invalid root scope")
	}
}

// Close waits for active operations before releasing the owner gate. Callers
// must not invoke it synchronously from a Read or Write stream callback.
func (r *Root) Close() error {
	r.once.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.closed = true
		r.close = r.root.Close()
		r.unlock()
	})
	return r.close
}

func cleanName(name string, dot bool) (string, error) {
	if name == "" || !filepath.IsLocal(name) || strings.ContainsRune(name, '\x00') || strings.Contains(name, `\`) || filepath.Clean(name) != name || (!dot && name == ".") {
		return "", errors.New("home: canonical relative name required")
	}
	return name, nil
}

func cleanSymlinkTarget(target string) (string, error) {
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsRune(target, '\x00') || strings.Contains(target, `\`) || path.Clean(target) != target || target == "." || target == ".." || strings.HasPrefix(target, "../") {
		return "", errors.New("home: canonical non-escaping relative symlink target required")
	}
	return target, nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("home: context is required")
	}
	return ctx.Err()
}

func (r *Root) writable() error {
	if r.access == RootReadOnly {
		return ErrReadOnly
	}
	return nil
}

func (r *Root) begin() error {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return fs.ErrClosed
	}
	return nil
}

func (r *Root) end() { r.mu.RUnlock() }

func (r *Root) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := r.begin(); err != nil {
		return nil, err
	}
	defer r.end()
	n, e := cleanName(name, true)
	if e != nil {
		return nil, e
	}
	return r.root.Stat(n)
}

func (r *Root) Lstat(ctx context.Context, name string) (fs.FileInfo, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := r.begin(); err != nil {
		return nil, err
	}
	defer r.end()
	n, err := cleanName(name, true)
	if err != nil {
		return nil, err
	}
	return r.root.Lstat(n)
}

func (r *Root) List(ctx context.Context, name string, o ListOptions) ([]fs.DirEntry, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := r.begin(); err != nil {
		return nil, err
	}
	defer r.end()
	n, e := cleanName(name, true)
	if e != nil {
		return nil, e
	}
	if o.Limit <= 0 {
		return nil, errors.New("home: positive list limit required")
	}
	dir, e := r.root.OpenRoot(n)
	if e != nil {
		return nil, e
	}
	defer func() { _ = dir.Close() }()
	f, e := dir.Open(".")
	if e != nil {
		return nil, e
	}
	defer func() { _ = f.Close() }()
	out, e := f.ReadDir(o.Limit)
	if e != nil && !errors.Is(e, io.EOF) {
		return nil, e
	}
	if len(out) == o.Limit {
		extra, readErr := f.ReadDir(1)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if len(extra) != 0 {
			return nil, ErrListLimit
		}
	}
	return out, nil
}

func (r *Root) Read(ctx context.Context, name string, dst io.Writer, o ReadOptions) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := r.begin(); err != nil {
		return err
	}
	defer r.end()
	if dst == nil {
		return errors.New("home: read destination is required")
	}
	if o.MaxBytes <= 0 {
		return errors.New("home: positive read limit required")
	}
	n, e := cleanName(name, true)
	if e != nil {
		return e
	}
	f, e := openRootFile(r.root, n, os.O_RDONLY, 0)
	if e != nil {
		return e
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() {
		return errors.New("home: read requires regular file")
	}
	reader := &contextReader{ctx, f}
	written, e := io.Copy(dst, io.LimitReader(reader, o.MaxBytes))
	if e != nil {
		return e
	}
	if written == o.MaxBytes {
		var extra [1]byte
		if count, readErr := reader.Read(extra[:]); count != 0 {
			return ErrReadLimit
		} else if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
	}
	closed = true
	return f.Close()
}

func (r *Root) Write(ctx context.Context, name string, src io.Reader, o WriteOptions) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := r.begin(); err != nil {
		return err
	}
	defer r.end()
	if e := r.writable(); e != nil {
		return e
	}
	if src == nil {
		return errors.New("home: write source is required")
	}
	n, e := cleanName(name, false)
	if e != nil {
		return e
	}
	mode := o.Mode.Perm()
	if mode == 0 {
		mode = 0o600
	}
	flags := os.O_WRONLY | os.O_CREATE
	if o.Exclusive {
		flags |= os.O_EXCL
	}
	if o.Append {
		flags |= os.O_APPEND
	}
	f, e := openRootFile(r.root, n, flags, mode)
	if e != nil {
		return e
	}
	if info, statErr := f.Stat(); statErr != nil {
		_ = f.Close()
		return fmt.Errorf("%w: inspect write target: %w", ErrOutcomeUnknown, statErr)
	} else if !info.Mode().IsRegular() {
		_ = f.Close()
		return errors.New("home: write requires regular file")
	} else if o.Exclusive && info.Mode().Perm() != mode {
		// Open creation modes are filtered through the process umask. Exclusive
		// immutable publications require the requested mode to be exact because
		// it participates in the revision digest, so restore it before syncing.
		if chmodErr := f.Chmod(mode); chmodErr != nil {
			_ = f.Close()
			return fmt.Errorf("%w: set exclusive write mode: %w", ErrOutcomeUnknown, chmodErr)
		}
	}
	if !o.Append {
		if truncateErr := f.Truncate(0); truncateErr != nil {
			_ = f.Close()
			return fmt.Errorf("%w: truncate target: %w", ErrOutcomeUnknown, truncateErr)
		}
	}
	_, copyErr := io.Copy(f, &contextReader{ctx, src})
	if copyErr == nil {
		copyErr = ctx.Err()
	}
	syncErr := error(nil)
	if copyErr == nil && o.Sync {
		syncErr = f.Sync()
	}
	closeErr := f.Close()
	if e = errors.Join(copyErr, syncErr, closeErr); e != nil {
		return fmt.Errorf("%w: %w", ErrOutcomeUnknown, e)
	}
	return e
}

func (r *Root) Upload(ctx context.Context, name string, src io.Reader, o WriteOptions) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := r.begin(); err != nil {
		return err
	}
	defer r.end()
	if err := r.writable(); err != nil {
		return err
	}
	if src == nil || o.Append {
		return errors.New("home: upload requires a non-append source")
	}
	if o.MaxBytes <= 0 {
		return errors.New("home: upload requires a positive byte limit")
	}
	target, err := cleanName(name, false)
	if err != nil {
		return err
	}
	mode := o.Mode.Perm()
	if mode == 0 {
		mode = 0o600
	}
	parent, base := filepath.Dir(target), filepath.Base(target)
	var temporary string
	var file *os.File
	for range 100 {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		temporary = filepath.Join(parent, "."+base+".upload-"+hex.EncodeToString(random[:]))
		file, err = openRootFile(r.root, temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		temporary = ""
	}
	if temporary == "" {
		return errors.New("home: allocate upload temporary file")
	}
	defer func() { _ = r.root.Remove(temporary) }()
	reader := &contextReader{ctx, src}
	_, copyErr := io.CopyN(file, reader, o.MaxBytes)
	if errors.Is(copyErr, io.EOF) {
		copyErr = nil
	} else if copyErr == nil {
		var probe [1]byte
		count, err := io.ReadFull(reader, probe[:])
		if count > 0 {
			copyErr = ErrUploadLimit
		} else if err != nil && !errors.Is(err, io.EOF) {
			copyErr = err
		}
	}
	if copyErr == nil {
		copyErr = ctx.Err()
	}
	syncErr := error(nil)
	if copyErr == nil && o.Sync {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := r.root.Rename(temporary, target); err != nil {
		return fmt.Errorf("%w: publish upload: %w", ErrOutcomeUnknown, err)
	}
	if o.Sync {
		if err := syncRootDirectory(r.root, parent); err != nil {
			return fmt.Errorf("%w: sync upload parent: %w", ErrOutcomeUnknown, err)
		}
	}
	return nil
}

func (r *Root) Mkdir(ctx context.Context, name string, mode fs.FileMode, o MkdirOptions) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := r.begin(); err != nil {
		return err
	}
	defer r.end()
	if e := r.writable(); e != nil {
		return e
	}
	n, e := cleanName(name, false)
	if e != nil {
		return e
	}
	mode = mode.Perm()
	if mode == 0 {
		mode = 0o755
	}
	if o.Parents {
		if e := r.root.MkdirAll(n, mode); e != nil {
			return fmt.Errorf("%w: create directory chain: %w", ErrOutcomeUnknown, e)
		}
		return nil
	}
	return r.root.Mkdir(n, mode)
}

func (r *Root) Remove(ctx context.Context, name string, o RemoveOptions) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := r.begin(); err != nil {
		return err
	}
	defer r.end()
	if e := r.writable(); e != nil {
		return e
	}
	n, e := cleanName(name, false)
	if e != nil {
		return e
	}
	if o.Recursive {
		if e := r.root.RemoveAll(n); e != nil {
			return fmt.Errorf("%w: remove recursively: %w", ErrOutcomeUnknown, e)
		}
		return nil
	}
	return r.root.Remove(n)
}

func (r *Root) Rename(ctx context.Context, old, new string, o RenameOptions) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := r.begin(); err != nil {
		return err
	}
	defer r.end()
	if e := r.writable(); e != nil {
		return e
	}
	a, e := cleanName(old, false)
	if e != nil {
		return e
	}
	b, e := cleanName(new, false)
	if e != nil {
		return e
	}
	if o.NoReplace {
		e = renameRootNoReplace(r.root, a, b)
	} else {
		e = r.root.Rename(a, b)
	}
	if e != nil {
		if o.NoReplace {
			return e
		}
		return fmt.Errorf("%w: rename: %w", ErrOutcomeUnknown, e)
	}
	if o.SyncParent {
		parents := []string{filepath.Dir(b)}
		if oldParent := filepath.Dir(a); oldParent != parents[0] {
			parents = append(parents, oldParent)
		}
		for _, parent := range parents {
			if e := syncRootDirectory(r.root, parent); e != nil {
				return fmt.Errorf("%w: sync rename parent: %w", ErrOutcomeUnknown, e)
			}
		}
	}
	return nil
}

func (r *Root) Symlink(ctx context.Context, target, name string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := r.begin(); err != nil {
		return err
	}
	defer r.end()
	if err := r.writable(); err != nil {
		return err
	}
	t, err := cleanSymlinkTarget(target)
	if err != nil {
		return err
	}
	n, err := cleanName(name, false)
	if err != nil {
		return err
	}
	return symlinkRoot(r.root, t, n)
}

func (r *Root) Readlink(ctx context.Context, name string) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}
	if err := r.begin(); err != nil {
		return "", err
	}
	defer r.end()
	n, err := cleanName(name, false)
	if err != nil {
		return "", err
	}
	return readlinkRoot(r.root, n)
}

func (r *Root) SyncDirectory(ctx context.Context, name string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := r.begin(); err != nil {
		return err
	}
	defer r.end()
	if err := r.writable(); err != nil {
		return err
	}
	n, err := cleanName(name, true)
	if err != nil {
		return err
	}
	if err := syncRootDirectory(r.root, n); err != nil {
		return fmt.Errorf("%w: sync directory: %w", ErrOutcomeUnknown, err)
	}
	return nil
}

func syncRootDirectory(root *os.Root, name string) error {
	dir, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	f, err := dir.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}
