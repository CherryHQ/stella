package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/vault"
)

const (
	defaultPort  = 25678
	stateVersion = 2
	// Covers embedded PostgreSQL's 45-second startup timeout so stop can wait
	// through an in-flight database start and still observe owned cleanup.
	shutdownWait = 60 * time.Second
)

type config struct {
	RepoRoot string
	Port     int
}

type processIdentity struct {
	PID     int    `json:"pid"`
	Started string `json:"started"`
}
type supervisorState struct {
	Version  int             `json:"version"`
	Owner    processIdentity `json:"owner"`
	Instance string          `json:"instance"`
	Root     string          `json:"root"`
}

func statePath(repoRoot string) string {
	abs, err := filepath.Abs(repoRoot)
	if err == nil {
		repoRoot = abs
	}
	sum := sha256.Sum256([]byte(filepath.Clean(repoRoot)))
	return filepath.Join(os.TempDir(), "stella-testbed-"+hex.EncodeToString(sum[:8])+".json")
}

func start(ctx context.Context, cfg config) (err error) {
	if _, err := os.Stat(binaryPath(cfg.RepoRoot)); err != nil {
		return fmt.Errorf("%s is missing (run via `mise run testbed:start`)", binaryPath(cfg.RepoRoot))
	}
	owner, err := currentIdentity()
	if err != nil {
		return fmt.Errorf("identify supervisor: %w", err)
	}
	instance, err := randomID()
	if err != nil {
		return fmt.Errorf("generate instance identity: %w", err)
	}
	root, err := os.MkdirTemp("", "stella-testbed-")
	if err != nil {
		return fmt.Errorf("create temporary test root: %w", err)
	}
	defer func() { _ = os.RemoveAll(root) }()

	state := supervisorState{Version: stateVersion, Owner: owner, Instance: instance, Root: root}
	path := statePath(cfg.RepoRoot)
	if err := claimOrRecover(path, state); err != nil {
		return err
	}
	defer func() { _ = removeOwnedState(path, state) }()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create temporary Stella home: %w", err)
	}
	if err := ensurePortAvailable(cfg.Port); err != nil {
		return err
	}

	logFile, err := os.OpenFile(filepath.Join(root, "stellad.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("create server log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	db, err := appdb.StartEmbedded("", 0)
	if err != nil {
		return fmt.Errorf("start isolated embedded postgres: %w", err)
	}
	defer func() {
		if stopErr := db.Stop(); err == nil && stopErr != nil {
			err = stopErr
		}
	}()
	vaultKey, err := vault.GenerateMasterIdentity()
	if err != nil {
		return fmt.Errorf("generate vault identity: %w", err)
	}

	cmd := exec.Command(binaryPath(cfg.RepoRoot), "server")
	cmd.Dir, cmd.Stdout, cmd.Stderr = cfg.RepoRoot, logFile, logFile
	cmd.Env = serverEnvironment(home, db.DSN(), vaultKey, cfg.Port)
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start stellad: %w", err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	defer stopServer(cmd, done)

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err = waitServerReady(readyCtx, baseURL, done)
	cancel()
	if err != nil {
		return err
	}
	if _, _, err := bootstrap(ctx, bootstrapConfig{BaseURL: baseURL, Home: home}); err != nil {
		return fmt.Errorf("bootstrap test identities: %w", err)
	}

	// stdout is deliberately limited to non-secret connection information.
	fmt.Println("Stella testbed:", baseURL)
	fmt.Println("Credentials:", filepath.Join(home, credentialsFilename))
	fmt.Println("Stop and clean up: mise run testbed:stop")
	select {
	case <-ctx.Done():
		return nil
	case <-done:
		return fmt.Errorf("stellad exited: %w", waitErr)
	}
}

func ensurePortAvailable(port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("testbed port %d is already in use; stop the existing server before starting an isolated testbed: %w", port, err)
	}
	if err := listener.Close(); err != nil {
		return fmt.Errorf("release testbed port %d preflight listener: %w", port, err)
	}
	return nil
}

func serverEnvironment(home, dsn, vaultKey string, port int) []string {
	// Sandbox backend selection is deploy-time and env-only, so the eval
	// harness must be able to hand the bridge backend through here; every
	// other STELLA_* value stays isolated. The docker backend also needs its
	// deploy-time mode (host/bind/volume), or it refuses to start.
	keep := []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "STELLA_SANDBOX_BACKEND", "STELLA_DOCKER_SANDBOX_MODE", "STELLA_EVAL_BRIDGE_DIR", "STELLA_EVAL_CODE_TOOL_SURFACE"}
	env := make([]string, 0, len(keep)+6)
	for _, name := range keep {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return append(env,
		"STELLA_HOME="+home,
		"STELLA_DATABASE_URL="+dsn,
		"STELLA_VAULT_KEY="+vaultKey,
		"STELLA_SERVER_URL="+fmt.Sprintf("http://127.0.0.1:%d", port),
		"HOST=127.0.0.1",
		fmt.Sprintf("PORT=%d", port),
	)
}

func waitServerReady(ctx context.Context, baseURL string, exited <-chan struct{}) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-exited:
			return errors.New("stellad exited before ready")
		default:
		}
		if err := ready(baseURL); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("server did not become ready: %w", ctx.Err())
		case <-exited:
			return errors.New("stellad exited before ready")
		case <-ticker.C:
		}
	}
}

func randomID() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

func claimOrRecover(path string, state supervisorState) error {
	err := publishState(path, state)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return err
	}
	existing, err := loadState(path)
	if err != nil {
		return fmt.Errorf("inspect existing supervisor state: %w", err)
	}
	live, err := identityFor(existing.Owner.PID)
	if err == nil && sameIdentity(existing.Owner, live) {
		return errors.New("a testbed is already active for this checkout")
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("validate existing supervisor: %w", err)
	}
	// A different start identity proves a reused PID. It is stale state, never
	// a reason to signal the unrelated process.
	if err := removeStaleState(path, existing); err != nil {
		return fmt.Errorf("remove stale supervisor state: %w", err)
	}
	return publishState(path, state)
}

func stop(ctx context.Context, path string) error {
	return stopWith(ctx, path, identityFor, signalProcess)
}

func stopWith(ctx context.Context, path string, identity func(int) (processIdentity, error), signal func(int) error) error {
	state, err := loadState(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	live, err := identity(state.Owner.PID)
	if errors.Is(err, fs.ErrNotExist) {
		return removeStaleState(path, state)
	}
	if err != nil {
		return fmt.Errorf("validate supervisor process: %w", err)
	}
	if !sameIdentity(state.Owner, live) {
		return removeStaleState(path, state)
	}
	if err := signal(state.Owner.PID); err != nil {
		return fmt.Errorf("signal supervisor: %w", err)
	}
	deadline := time.NewTimer(shutdownWait)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("supervisor did not stop before cleanup deadline")
		case <-time.After(100 * time.Millisecond):
		}
		live, err := identity(state.Owner.PID)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wait for supervisor: %w", err)
		}
		if !sameIdentity(state.Owner, live) {
			return nil
		}
	}
}

func loadState(path string) (supervisorState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return supervisorState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return supervisorState{}, errors.New("supervisor state must be a regular mode 0600 file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return supervisorState{}, err
	}
	var state supervisorState
	if err := json.Unmarshal(data, &state); err != nil || state.Version != stateVersion || state.Owner.PID < 1 || state.Owner.Started == "" || state.Instance == "" || !safeInstanceRoot(state.Root) {
		return supervisorState{}, errors.New("supervisor state is invalid")
	}
	return state, nil
}

func publishState(path string, state supervisorState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".stella-testbed-state-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func safeInstanceRoot(root string) bool {
	root, err := filepath.Abs(root)
	if err != nil || !strings.HasPrefix(filepath.Base(root), "stella-testbed-") {
		return false
	}
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(temp, root)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func removeStaleState(path string, state supervisorState) error {
	if !safeInstanceRoot(state.Root) {
		return errors.New("supervisor state has unsafe temporary root")
	}
	if err := os.RemoveAll(state.Root); err != nil {
		return fmt.Errorf("remove stale test root: %w", err)
	}
	return removeOwnedState(path, state)
}

func removeOwnedState(path string, want supervisorState) error {
	got, err := loadState(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("supervisor state changed ownership; refusing to remove it")
	}
	return os.Remove(path)
}
