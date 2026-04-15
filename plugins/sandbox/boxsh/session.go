package boxsh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
	"github.com/vaayne/anna/plugins/sandbox/boxsh/boxshclient"
)

type (
	Policy                   = sandboxpkg.Policy
	FilesystemPolicy         = sandboxpkg.FilesystemPolicy
	Session                  = sandboxpkg.Session
	Host                     = sandboxpkg.Host
	ReadResult               = sandboxpkg.ReadResult
	WriteResult              = sandboxpkg.WriteResult
	Edit                     = sandboxpkg.Edit
	EditResult               = sandboxpkg.EditResult
	StatResult               = sandboxpkg.StatResult
	DirEntry                 = sandboxpkg.DirEntry
	TempFile                 = sandboxpkg.TempFile
	ExecOptions              = sandboxpkg.ExecOptions
	ExecResult               = sandboxpkg.ExecResult
	ProcessRequest           = sandboxpkg.ProcessRequest
	ProcessHandle            = sandboxpkg.ProcessHandle
	HTTPOptions              = sandboxpkg.HTTPOptions
	HTTPResult               = sandboxpkg.HTTPResult
	HTTPStream               = sandboxpkg.HTTPStream
	PolicyCompatibilityError = sandboxpkg.PolicyCompatibilityError
)

func nextSessionID() string { return sandboxpkg.NewSessionID() }

func logSessionCreated(sessionID, backend string, policy Policy) {
	sandboxpkg.LogSessionCreated(sessionID, backend, policy)
}

func logSessionClosed(sessionID, backend, reason string) {
	sandboxpkg.LogSessionClosed(sessionID, backend, reason)
}

func logRelaxedMode(sessionID, backend, reason string, policy Policy, warnings ...string) {
	sandboxpkg.LogRelaxedMode(sessionID, backend, reason, policy, warnings...)
}

func logPolicyDenied(sessionID, backend, operation, resource, reason string) {
	sandboxpkg.LogPolicyDenied(sessionID, backend, operation, resource, reason)
}

func PlatformRequiresBoxsh() bool {
	switch runtime.GOOS {
	case "linux", "darwin":
		return true
	default:
		return false
	}
}

// boxshFactory creates boxsh-backed sandbox sessions.
// This factory is only available on platforms that support boxsh.
type boxshFactory struct{}

func NewFactory() sandboxpkg.Factory { return &boxshFactory{} }

func (f *boxshFactory) Name() string { return "boxsh" }

func (f *boxshFactory) Available() bool {
	return PlatformRequiresBoxsh()
}

func (f *boxshFactory) Supported(policy Policy) error {
	if !f.Available() {
		return &PolicyCompatibilityError{
			Backend:          f.Name(),
			Policy:           policy,
			Reason:           "boxsh is not available on this platform",
			RelaxedWouldHelp: false,
		}
	}

	// Check network whitelist support
	// boxsh 2.0.1 doesn't support whitelist mode
	if policy.RequiresWhitelist() && !policy.Relaxed {
		return &PolicyCompatibilityError{
			Backend:          f.Name(),
			Policy:           policy,
			Reason:           "boxsh does not support network whitelist mode",
			RelaxedWouldHelp: true,
		}
	}

	// All other policies are supported by boxsh
	return nil
}

func (f *boxshFactory) CreateSession(ctx context.Context, policy Policy) (Session, error) {
	if err := f.Supported(policy); err != nil {
		return nil, err
	}

	annaHome := policy.Process.Environment["ANNA_HOME"]
	if annaHome == "" {
		annaHome = boxshclient.DefaultAnnaHome()
	}
	binaryPath, err := boxshclient.ResolveManagedBoxshPath(annaHome)
	if err != nil {
		return nil, fmt.Errorf("boxsh session: %w", err)
	}

	backendCfg := boxshclient.BackendConfig{
		AnnaHome:     annaHome,
		BinaryPath:   binaryPath,
		UserRoot:     policy.WorkspaceRootOrDefault(),
		WorkDir:      policy.Filesystem.WorkingDir,
		ReadOnlyDirs: policy.Filesystem.ReadOnlyPaths,
		Sandbox: boxshclient.NetworkConfig{
			Mode:      string(policy.NetworkModeOrDefault()),
			Allowlist: policy.Network.Allowlist,
		},
	}
	if policy.RequiresWhitelist() && policy.Relaxed {
		backendCfg.Sandbox.Mode = boxshclient.NetworkAllowAll
	}

	sessionID := nextSessionID()
	ctx, span := tracer.Start(ctx, "sandbox.boxsh.session",
		trace.WithAttributes(sessionTraceAttrs(sessionID, policy, backendCfg)...),
	)

	backend, err := boxshclient.NewSharedBackend(backendCfg)
	if err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("boxsh session: %w", err)
	}
	if err := backend.Start(ctx, backendCfg); err != nil {
		recordError(span, err)
		span.End()
		return nil, fmt.Errorf("boxsh session: start backend: %w", err)
	}
	span.AddEvent("sandbox.boxsh.session.ready", trace.WithAttributes(
		attribute.String("anna.sandbox.binary", binaryPath),
	))

	session := &boxshSession{
		id:        sessionID,
		policy:    policy,
		backend:   backend,
		client:    backend.Client(),
		done:      make(chan struct{}),
		traceSpan: span,
	}
	if policy.RequiresWhitelist() && policy.Relaxed {
		logRelaxedMode(session.id, f.Name(), "boxsh whitelist mode relaxed to allow_all", policy, "network whitelist treated as allow_all")
	}
	logSessionCreated(session.id, "boxsh", policy)
	go session.watchBackend()
	return session, nil
}

// boxshSession is a boxsh-backed sandbox session.
type boxshSession struct {
	id        string
	policy    Policy
	backend   *boxshclient.SharedBackend
	client    *boxshclient.Client
	host      *boxshHost
	done      chan struct{}
	doneOnce  sync.Once
	closed    bool
	closeErr  error
	traceSpan trace.Span
	traceOnce sync.Once
	mu        sync.RWMutex
}

func (s *boxshSession) Host() Host {
	if s.host == nil {
		s.host = &boxshHost{session: s}
	}
	return s.host
}

func (s *boxshSession) Policy() Policy { return s.policy }

func (s *boxshSession) Alive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed || s.client == nil {
		return false
	}

	return s.client.Alive()
}

func (s *boxshSession) Done() <-chan struct{} { return s.done }

func (s *boxshSession) closeDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *boxshSession) watchBackend() {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.RLock()
		closed := s.closed
		backend := s.backend
		s.mu.RUnlock()
		if closed {
			s.closeDone()
			return
		}
		if backend == nil || !backend.Alive() {
			err := fmt.Errorf("boxsh backend liveness lost")
			s.endTrace("liveness_lost", err)
			logSessionClosed(s.id, "boxsh", "liveness_lost")
			s.closeDone()
			return
		}
	}
}

func (s *boxshSession) endTrace(reason string, err error) {
	s.traceOnce.Do(func() {
		if s.traceSpan == nil {
			return
		}
		s.traceSpan.AddEvent("sandbox.boxsh.session.closed", trace.WithAttributes(
			attribute.String("anna.sandbox.close_reason", reason),
		))
		recordError(s.traceSpan, err)
		s.traceSpan.End()
	})
}

func (s *boxshSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return s.closeErr
	}

	s.closed = true

	if s.backend != nil {
		s.closeErr = s.backend.Close()
	} else if s.client != nil {
		s.closeErr = s.client.Close()
	}

	s.closeDone()
	s.endTrace("explicit_close", s.closeErr)
	logSessionClosed(s.id, "boxsh", "explicit_close")
	return s.closeErr
}

// boxshHost implements Host interface using boxsh RPC calls.
type boxshHost struct {
	session *boxshSession
}

func (h *boxshHost) ReadFile(ctx context.Context, path string, offset, limit int) (ReadResult, error) {
	content, err := h.ReadAllFile(ctx, path)
	if err != nil {
		return ReadResult{}, err
	}

	if offset > 0 {
		if offset >= len(content) {
			return ReadResult{Content: nil, NextOffset: offset}, nil
		}
		content = content[offset:]
	}

	truncated := false
	if limit > 0 && len(content) > limit {
		content = content[:limit]
		truncated = true
	}

	nextOffset := offset + len(content)
	if truncated {
		nextOffset = offset + limit
	}

	return ReadResult{
		Content:    content,
		Truncated:  truncated,
		NextOffset: nextOffset,
	}, nil
}

func (h *boxshHost) readFileLines(ctx context.Context, path string, offset, limit int) (ReadResult, error) {
	client := h.session.client
	if client == nil {
		return ReadResult{}, fmt.Errorf("boxsh host: session not available")
	}

	// Use boxsh client for file read
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return ReadResult{}, err
	}
	if offset <= 0 {
		offset = 1
	}
	result, err := client.Read(ctx, boxshclient.ReadParams{FilePath: resolved, Offset: offset, Limit: limit})
	if err != nil {
		return ReadResult{}, err
	}

	lineCount := strings.Count(result.Content, "\n")
	if result.Content != "" && !strings.HasSuffix(result.Content, "\n") {
		lineCount++
	}

	return ReadResult{
		Content:    []byte(result.Content),
		Truncated:  result.Truncated,
		NextOffset: offset + max(lineCount, 1),
	}, nil
}

func (h *boxshHost) WriteFile(ctx context.Context, path string, content []byte) (WriteResult, error) {
	client := h.session.client
	if client == nil {
		return WriteResult{}, fmt.Errorf("boxsh host: session not available")
	}

	if err := h.ensureWritable(path); err != nil {
		return WriteResult{}, err
	}

	// Use boxsh client for file write
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return WriteResult{}, err
	}
	result, err := client.Write(ctx, boxshclient.WriteParams{FilePath: resolved, Content: string(content)})
	if err != nil {
		return WriteResult{}, err
	}

	return WriteResult{BytesWritten: result.BytesWritten}, nil
}

func (h *boxshHost) EditFile(ctx context.Context, path string, edits []Edit) (EditResult, error) {
	if err := h.ensureWritable(path); err != nil {
		return EditResult{}, err
	}
	client := h.session.client
	if client == nil {
		return EditResult{}, fmt.Errorf("boxsh host: session not available")
	}

	resolved, err := h.ResolvePath(path)
	if err != nil {
		return EditResult{}, err
	}

	specs := make([]boxshclient.EditSpec, 0, len(edits))
	for _, edit := range edits {
		specs = append(specs, boxshclient.EditSpec{OldText: edit.OldText, NewText: edit.NewText})
	}
	result, err := client.Edit(ctx, boxshclient.EditParams{FilePath: resolved, Edits: specs})
	if err != nil {
		return EditResult{}, err
	}
	return EditResult{AppliedEdits: result.Replacements}, nil
}

func (h *boxshHost) Stat(ctx context.Context, path string) (StatResult, error) {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return StatResult{}, err
	}

	result, err := h.execSandbox(ctx, "stat", boxshStatCommand(resolved))
	if err != nil {
		return StatResult{}, err
	}
	return parseBoxshStat(result.Stdout)
}

func (h *boxshHost) ListDir(ctx context.Context, path string) ([]DirEntry, error) {
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return nil, err
	}

	execResult, err := h.execSandbox(ctx, "list", boxshListDirCommand(resolved))
	if err != nil {
		return nil, err
	}
	return parseBoxshListDir(execResult.Stdout)
}

func (h *boxshHost) MkdirAll(ctx context.Context, path string, perm uint32) error {
	if err := h.ensureWritable(path); err != nil {
		return err
	}
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return err
	}

	_ = perm
	_, err = h.execSandbox(ctx, "mkdir", "p="+shellQuote(resolved)+`; mkdir -p "$p"`)
	return err
}

func (h *boxshHost) Remove(ctx context.Context, path string, recursive bool) error {
	if err := h.ensureWritable(path); err != nil {
		return err
	}
	resolved, err := h.ResolvePath(path)
	if err != nil {
		return err
	}

	var command string
	if recursive {
		command = "p=" + shellQuote(resolved) + `; rm -rf "$p"`
	} else {
		command = "p=" + shellQuote(resolved) + `; rm "$p"`
	}
	_, err = h.execSandbox(ctx, "remove", command)
	return err
}

func (h *boxshHost) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := h.ensureWritable(oldPath); err != nil {
		return err
	}
	if err := h.ensureWritable(newPath); err != nil {
		return err
	}
	resolvedOld, err := h.ResolvePath(oldPath)
	if err != nil {
		return err
	}

	resolvedNew, err := h.ResolvePath(newPath)
	if err != nil {
		return err
	}

	_, err = h.execSandbox(ctx, "rename", "old="+shellQuote(resolvedOld)+"; new="+shellQuote(resolvedNew)+`; mv "$old" "$new"`)
	return err
}

func (h *boxshHost) CreateTemp(ctx context.Context, dir, pattern string) (TempFile, error) {
	policy := h.session.policy.Filesystem

	resolvedDir := dir
	if resolvedDir == "" {
		resolvedDir = policy.WorkingDir
	}
	if err := h.ensureWritable(resolvedDir); err != nil {
		return nil, err
	}
	resolvedDir, err := h.ResolvePath(resolvedDir)
	if err != nil {
		return nil, err
	}

	template := mktempTemplate(pattern)
	result, err := h.execSandbox(ctx, "mktemp", "dir="+shellQuote(resolvedDir)+"; mkdir -p \"$dir\"; mktemp "+shellQuote(filepath.Join(resolvedDir, template)))
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(result.Stdout)
	if path == "" {
		return nil, fmt.Errorf("boxsh host: mktemp returned empty path")
	}
	return &boxshTempFile{host: h, path: path}, nil
}

func (h *boxshHost) Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error) {
	policy := h.session.policy.Process

	// Determine working directory
	cwd := opts.Cwd
	if cwd == "" {
		cwd = h.session.policy.Filesystem.WorkingDir
	}
	cwd, err := h.ResolvePath(cwd)
	if err != nil {
		return ExecResult{}, err
	}

	// Merge environment
	env := os.Environ()
	if !policy.InheritEnv {
		env = nil
	}
	for k, v := range policy.Environment {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	for k, v := range opts.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Determine timeout
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = policy.Timeout
	}

	// Apply timeout to context
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if h.hasReadOnlyOverlap() {
		logPolicyDenied(h.session.id, "boxsh", "exec", command, "read-only overlap prevents safe execution")
		return ExecResult{}, fmt.Errorf("sandbox: boxsh Host.Exec is fail-closed when ReadOnlyPaths overlap WorkspaceRoot")
	}

	// Use boxsh client for sandboxed execution
	client := h.session.client
	if client == nil {
		return ExecResult{}, fmt.Errorf("boxsh host: session not available")
	}

	if len(env) > 0 {
		// Current boxsh RPC only accepts a shell command, so env/cwd are encoded in the command.
		prefix := strings.Join(func() []string {
			parts := make([]string, 0, len(env))
			for _, kv := range env {
				parts = append(parts, fmt.Sprintf("export %s;", kv))
			}
			return parts
		}(), " ")
		command = prefix + " cd " + shellQuote(cwd) + " && " + command
	} else {
		command = "cd " + shellQuote(cwd) + " && " + command
	}

	result, err := client.Exec(ctx, boxshclient.ExecParams{Command: command, Timeout: int(timeout.Seconds())})
	if err != nil {
		return ExecResult{}, err
	}

	return ExecResult{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode}, nil
}

func (h *boxshHost) StartProcess(_ context.Context, req ProcessRequest) (ProcessHandle, error) {
	logPolicyDenied(h.session.id, "boxsh", "start_process", req.Path, "transport mediation not yet implemented")
	return nil, fmt.Errorf("sandbox: boxsh Host.StartProcess is not implemented; fail closed until transport mediation is wired")
}

func (h *boxshHost) HTTPRequest(_ context.Context, opts HTTPOptions) (HTTPResult, error) {
	logPolicyDenied(h.session.id, "boxsh", "http_request", opts.URL, "transport mediation not yet implemented")
	return HTTPResult{}, fmt.Errorf("sandbox: boxsh Host.HTTPRequest is not implemented; fail closed until transport mediation is wired")
}

func (h *boxshHost) OpenHTTPStream(_ context.Context, opts HTTPOptions) (HTTPStream, error) {
	logPolicyDenied(h.session.id, "boxsh", "http_stream", opts.URL, "transport mediation not yet implemented")
	return nil, fmt.Errorf("sandbox: boxsh Host.OpenHTTPStream is not implemented; fail closed until transport mediation is wired")
}

func (h *boxshHost) ResolvePath(path string) (string, error) {
	root, err := h.session.backend.UserRoot(context.Background())
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(h.session.policy.Filesystem.WorkingDir, path)
	}

	srcRoot := h.session.policy.WorkspaceRootOrDefault()
	if isWithinRoot(root, path) {
		return path, nil
	}
	if isWithinRoot(srcRoot, path) {
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return "", err
		}
		return filepath.Join(root, rel), nil
	}
	for _, ro := range h.session.policy.Filesystem.ReadOnlyPaths {
		if isWithinRoot(ro, path) {
			return path, nil
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Join(root, strings.TrimPrefix(path, string(filepath.Separator))), nil
	}
	if err := boxshclient.ValidatePathWithinRoot(root, path); err != nil {
		return "", err
	}
	return path, nil
}

func isWithinRoot(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (h *boxshHost) ensureWritable(path string) error {
	if h.hasReadOnlyOverlap() {
		logPolicyDenied(h.session.id, "boxsh", "write", path, "read-only overlap prevents safe mutation")
		return fmt.Errorf("sandbox: boxsh mutating host operations are fail-closed when ReadOnlyPaths overlap WorkspaceRoot")
	}
	logicalPath := path
	if !filepath.IsAbs(logicalPath) {
		logicalPath = filepath.Join(h.session.policy.Filesystem.WorkingDir, logicalPath)
	}
	for _, ro := range h.session.policy.Filesystem.ReadOnlyPaths {
		if isWithinRoot(ro, logicalPath) {
			logPolicyDenied(h.session.id, "boxsh", "write", path, "path is read-only in boxsh session")
			return fmt.Errorf("sandbox: path %q is read-only in boxsh session", path)
		}
	}
	return nil
}

func (h *boxshHost) hasReadOnlyOverlap() bool {
	workspaceRoot := h.session.policy.WorkspaceRootOrDefault()
	for _, ro := range h.session.policy.Filesystem.ReadOnlyPaths {
		if isWithinRoot(workspaceRoot, ro) || isWithinRoot(ro, workspaceRoot) {
			return true
		}
	}
	return false
}

func (h *boxshHost) WorkingDir() string {
	return h.session.policy.Filesystem.WorkingDir
}

func (h *boxshHost) ReadFileLines(ctx context.Context, path string, offset, limit int) (ReadResult, error) {
	return h.readFileLines(ctx, path, offset, limit)
}

func (h *boxshHost) ReadAllFile(ctx context.Context, path string) ([]byte, error) {
	return h.readAllFile(ctx, path)
}

func (h *boxshHost) readAllFile(ctx context.Context, path string) ([]byte, error) {
	offset := 1
	var out strings.Builder
	for {
		result, err := h.readFileLines(ctx, path, offset, 0)
		if err != nil {
			return nil, err
		}
		out.Write(result.Content)
		if !result.Truncated {
			break
		}
		nextOffset := result.NextOffset
		if nextOffset <= offset {
			lines := strings.Count(string(result.Content), "\n")
			if len(result.Content) > 0 && !strings.HasSuffix(string(result.Content), "\n") {
				lines++
			}
			nextOffset = offset + max(lines, 1)
		}
		offset = nextOffset
	}
	return []byte(out.String()), nil
}

// boxshTempFile implements TempFile for boxsh sessions.
type boxshTempFile struct {
	host    *boxshHost
	path    string
	content []byte
	closed  bool
	mu      sync.Mutex
}

func (f *boxshTempFile) Path() string { return f.path }
func (f *boxshTempFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return 0, fmt.Errorf("boxsh temp file: file is closed")
	}
	f.content = append(f.content, p...)
	if _, err := f.host.WriteFile(context.Background(), f.path, f.content); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (f *boxshTempFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return nil
	}
	f.closed = true
	return f.host.Remove(context.Background(), f.path, false)
}

func (h *boxshHost) execSandbox(ctx context.Context, op, command string) (*boxshclient.ExecResult, error) {
	client := h.session.client
	if client == nil {
		return nil, fmt.Errorf("boxsh host: session not available")
	}
	result, err := client.Exec(ctx, boxshclient.ExecParams{Command: command})
	if err != nil {
		return nil, fmt.Errorf("boxsh host %s: %w", op, err)
	}
	if result.ExitCode != 0 {
		output := strings.TrimSpace(result.Stderr)
		if output == "" {
			output = strings.TrimSpace(result.Stdout)
		}
		if output == "" {
			output = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return nil, fmt.Errorf("boxsh host %s: %s", op, output)
	}
	return result, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func boxshStatCommand(path string) string {
	return "p=" + shellQuote(path) + `; if [ ! -e "$p" ]; then printf '0\t0\t0\t0\t0\n'; else isdir=0; if [ -d "$p" ]; then isdir=1; fi; size=$( (stat -c %s "$p" 2>/dev/null || stat -f %z "$p" 2>/dev/null || printf 0) | head -n1); mode=$( (stat -c %a "$p" 2>/dev/null || stat -f %Lp "$p" 2>/dev/null || printf 0) | head -n1); mtime=$( (stat -c %Y "$p" 2>/dev/null || stat -f %m "$p" 2>/dev/null || printf 0) | head -n1); printf '1\t%s\t%s\t%s\t%s\n' "$isdir" "$size" "$mode" "$mtime"; fi`
}

func parseBoxshStat(stdout string) (StatResult, error) {
	fields := strings.Split(strings.TrimSpace(stdout), "\t")
	if len(fields) < 5 {
		return StatResult{}, fmt.Errorf("boxsh host: malformed stat response %q", stdout)
	}
	if fields[0] != "1" {
		return StatResult{Exists: false}, nil
	}

	size, _ := strconv.ParseInt(fields[2], 10, 64)
	mode, _ := strconv.ParseUint(fields[3], 8, 32)
	mtime, _ := strconv.ParseInt(fields[4], 10, 64)
	return StatResult{
		Exists:  true,
		IsDir:   fields[1] == "1",
		Size:    size,
		Mode:    uint32(mode),
		ModTime: time.Unix(mtime, 0),
	}, nil
}

func boxshListDirCommand(path string) string {
	return "p=" + shellQuote(path) + `; if [ ! -d "$p" ]; then printf 'not a directory: %s\n' "$p" >&2; exit 1; fi; for x in "$p"/* "$p"/.[!.]* "$p"/..?*; do [ -e "$x" ] || continue; name=${x##*/}; isdir=0; if [ -d "$x" ]; then isdir=1; fi; size=$( (stat -c %s "$x" 2>/dev/null || stat -f %z "$x" 2>/dev/null || printf 0) | head -n1); printf '%s\t%s\t%s\n' "$name" "$isdir" "$size"; done`
}

func parseBoxshListDir(stdout string) ([]DirEntry, error) {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	result := make([]DirEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			return nil, fmt.Errorf("boxsh host: malformed list response %q", line)
		}
		size, _ := strconv.ParseInt(fields[2], 10, 64)
		result = append(result, DirEntry{
			Name:  fields[0],
			IsDir: fields[1] == "1",
			Size:  size,
		})
	}
	return result, nil
}

func mktempTemplate(pattern string) string {
	if pattern == "" {
		return "tmp.XXXXXX"
	}
	if strings.Contains(pattern, "*") {
		return strings.Replace(pattern, "*", "XXXXXX", 1)
	}
	return pattern + ".XXXXXX"
}
