//go:build system

// Package system boots the real stellad binary as a subprocess and drives it
// over TCP, proving the process-level seams (startup, real HTTP auth, SSE
// transport, asynchronous workers, shutdown) that in-process tests cannot
// cover. Run it with `mise run system-test`; plain `go test ./...` never
// discovers this package. See web/content/docs/development/rules for the
// system-test policy.
package system

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/pgruntime"
	"github.com/CherryHQ/stella/internal/vault"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

const (
	// readyTimeout bounds the /readyz poll after the subprocess starts. First
	// boot migrates the database and syncs embedded assets, so it is generous;
	// early subprocess exit is detected immediately and never waits this long.
	readyTimeout = 120 * time.Second
	// gracefulTimeout bounds how long teardown waits for the server to drain
	// after the graceful termination signal before killing its process group.
	// It must exceed the server's HTTP shutdown and River soft-stop budgets.
	gracefulTimeout = 45 * time.Second
	// startupAttempts bounds retries when the free-port pick races another
	// process binding the same port between selection and server bind.
	startupAttempts = 3
)

// harness owns every resource of one system-test run: the stellad subprocess
// (which owns embedded PostgreSQL), an HTTP client with a cookie jar, and a
// direct database pool for invariants the API does not expose. Its cleanup
// follows the current proc and db fields so an embedded restart cannot bind
// replacement resources to the shorter-lived restart subtest.
type harness struct {
	runID    string
	home     string
	vaultKey string
	baseURL  string
	client   *http.Client
	db       *pgxpool.Pool
	proc     *serverProcess
}

// newHarness starts the full system under test or skips on hosts without an
// embedded PostgreSQL runtime. It returns only after /readyz reports 200.
func newHarness(t *testing.T) *harness {
	t.Helper()
	skipUnsupportedHost(t)

	runID := newRunID(t)

	vaultKey, err := vault.GenerateMasterIdentity()
	if err != nil {
		t.Fatalf("system: generate vault key: %v", err)
	}
	home := t.TempDir()

	// Do not set STELLA_DATABASE_URL: the candidate process must own the
	// embedded PostgreSQL lifecycle that a normal single-node installation uses.
	proc, baseURL := startServer(t, runID, home, vaultKey)
	h := &harness{
		runID: runID, home: home, vaultKey: vaultKey,
		baseURL: baseURL, proc: proc,
	}
	// This cleanup is owned by TestSystem, not one of its subtests. It follows
	// replacements assigned by the restart journey and remains safe after an
	// explicit stop/close because both operations are idempotent.
	t.Cleanup(func() {
		if h.proc != nil {
			h.proc.stop(t)
		}
		if h.db != nil {
			h.db.Close()
		}
	})

	db, err := pgxpool.New(context.Background(), embeddedDSN(t, home))
	if err != nil {
		t.Fatalf("system: connect assertion pool: %v", err)
	}
	h.db = db

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("system: cookie jar: %v", err)
	}
	// No client-wide timeout: SSE journeys hold one response open for the whole
	// stream. Every request must carry its own context deadline instead.
	h.client = &http.Client{Jar: jar}

	return h
}

// startServer boots the stellad subprocess and waits for readiness, retrying
// with a fresh port when the free-port pick loses its bind race.
func startServer(t *testing.T, runID, home, vaultKey string) (*serverProcess, string) {
	t.Helper()
	for attempt := 1; ; attempt++ {
		port := freePort(t)
		baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
		env := append(baseSubprocessEnv(),
			"STELLA_HOME="+home,
			"STELLA_POSTGRES_RUNTIME="+installedRuntimeRoot(t),
			"STELLA_VAULT_KEY="+vaultKey,
			"HOST=127.0.0.1",
			fmt.Sprintf("PORT=%d", port),
			"LOG_LEVEL=debug",
		)
		proc := startServerProcess(t, fmt.Sprintf("server-%s-a%d", runID, attempt), env)
		err := proc.waitReady(baseURL, readyTimeout)
		if err == nil {
			return proc, baseURL
		}
		proc.stop(t)
		if attempt < startupAttempts && proc.exited() && strings.Contains(proc.logTail(80), "address already in use") {
			t.Logf("system: port %d bind race, retrying: %v", port, err)
			continue
		}
		t.Fatalf("system: server never became ready: %v", err)
	}
}

// installedRuntimeRoot resolves the fixed Stella PostgreSQL runtime downloaded
// by the system-test task. The candidate receives this immutable runtime while
// keeping its own fresh STELLA_HOME and database directory.
func installedRuntimeRoot(t *testing.T) string {
	t.Helper()
	source, ok := pgruntime.DefaultRuntimeSource()
	if !ok {
		t.Fatalf("system: no PostgreSQL runtime source for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("system: resolve user home for PostgreSQL runtime: %v", err)
	}
	root := pgruntime.RuntimeRoot(filepath.Join(userHome, ".stella"), source)
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("system: PostgreSQL runtime not installed at %s (run `mise run pg:runtime:download`): %v", root, err)
	}
	if !info.IsDir() {
		t.Fatalf("system: PostgreSQL runtime path %s is not a directory", root)
	}
	return root
}

// embeddedDSN reads the postmaster metadata written below STELLA_HOME after
// /readyz succeeds. This lets assertions inspect the database started by the
// candidate itself without injecting an external DSN into that candidate.
func embeddedDSN(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, "postgres", "postmaster.pid")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("system: read embedded PostgreSQL metadata %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 4 {
		t.Fatalf("system: embedded PostgreSQL metadata %s has %d lines, want at least 4", path, len(lines))
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[3]))
	if err != nil || port < 1 || port > 65535 {
		t.Fatalf("system: invalid embedded PostgreSQL port %q in %s", lines[3], path)
	}
	return fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/stella?sslmode=disable", port)
}

// skipUnsupportedHost skips before any resource is acquired on platforms where
// the embedded PostgreSQL runtime is not published. The supported set is owned
// by internal/pgruntime; this must not duplicate its platform list.
func skipUnsupportedHost(t *testing.T) {
	t.Helper()
	if _, ok := pgruntime.DefaultRuntimeSource(); !ok {
		t.Skipf("system: no embedded PostgreSQL runtime is published for %s/%s; %s",
			runtime.GOOS, runtime.GOARCH, pgruntime.MissingRuntimeHint())
	}
}

// baseSubprocessEnv is an explicit allowlist: the subprocess never inherits
// the developer's full environment, so local STELLA_*/OTEL_*/AUTH_* settings
// cannot leak into a run and make it nondeterministic.
func baseSubprocessEnv() []string {
	keep := []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL"}
	env := make([]string, 0, len(keep)+8)
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// serverProcess tracks one stellad subprocess: its command, captured log, and
// a channel closed once Wait has reaped it.
type serverProcess struct {
	cmd     *exec.Cmd
	logPath string
	done    chan struct{}
	waitErr error
}

// startServerProcess launches `stellad serve` in its own process group with
// stdout/stderr captured to a per-run log under dist/logs/system-test/. The
// log outlives the run so failures can always point at it.
func startServerProcess(t *testing.T, logName string, env []string) *serverProcess {
	t.Helper()
	logPath := filepath.Join(logDir(t), logName+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("system: create server log %s: %v", logPath, err)
	}
	cmd := exec.Command(binaryPath(t), "serve")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = env
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("system: start %s: %v", cmd.Path, err)
	}
	// The child holds its own descriptor after Start.
	_ = logFile.Close()

	p := &serverProcess{cmd: cmd, logPath: logPath, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	return p
}

// waitReady polls /readyz until it returns 200, the subprocess exits, or the
// deadline passes. Early exit is reported immediately with the exit error, the
// log path, and a bounded log tail — never after the full readiness timeout.
func (p *serverProcess) waitReady(baseURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-p.done:
			return fmt.Errorf("server exited before ready: %w\nserver log: %s\n%s",
				p.waitErr, p.logPath, p.logTail(40))
		default:
		}
		resp, err := client.Get(baseURL + "/readyz")
		if err == nil {
			ok := resp.StatusCode == http.StatusOK
			_ = resp.Body.Close()
			if ok {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("server not ready within %s\nserver log: %s\n%s",
				timeout, p.logPath, p.logTail(40))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stop tears the subprocess down: graceful signal first, bounded wait, then a
// process-group kill. It always proves the owned process group is gone rather
// than trusting Kill, and it is idempotent so cleanup and explicit calls can
// both run it.
func (p *serverProcess) stop(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		// Already exited and reaped.
	default:
		if err := terminate(p.cmd.Process); err != nil {
			t.Logf("system: graceful signal failed, killing group: %v", err)
			killProcessGroup(p.cmd)
		}
		select {
		case <-p.done:
			if p.waitErr != nil {
				t.Errorf("system: server exited non-zero on graceful shutdown: %v\nserver log: %s\n%s",
					p.waitErr, p.logPath, p.logTail(40))
			}
		case <-time.After(gracefulTimeout):
			t.Errorf("system: server did not exit within %s of graceful signal; killing process group (server log: %s)",
				gracefulTimeout, p.logPath)
			killProcessGroup(p.cmd)
			select {
			case <-p.done:
			case <-time.After(10 * time.Second):
				t.Fatalf("system: server survived process-group kill (pid %d, server log: %s)",
					p.cmd.Process.Pid, p.logPath)
			}
		}
	}
	if processGroupAlive(p.cmd) {
		killProcessGroup(p.cmd)
		t.Errorf("system: processes left in the server's process group after shutdown (server log: %s)", p.logPath)
	}
}

// exited reports whether the subprocess has terminated and been reaped.
func (p *serverProcess) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// logTail returns up to n trailing lines of the server log for failure
// messages. Server logs never contain the vault key or credentials, so the
// tail is safe to embed in test output.
func (p *serverProcess) logTail(n int) string {
	data, err := os.ReadFile(p.logPath)
	if err != nil {
		return fmt.Sprintf("(read server log: %v)", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "server log tail:\n" + strings.Join(lines, "\n")
}

// binaryPath locates the freshly built stellad binary. The mise task builds it
// before running the suite; a stale or missing binary is a hard failure, not a
// skip, because testing an old binary would silently prove nothing.
func binaryPath(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("STELLA_SYSTEM_BINARY")
	if bin != "" && !filepath.IsAbs(bin) {
		t.Fatalf("system: STELLA_SYSTEM_BINARY must be absolute, got %q", bin)
	}
	if bin == "" {
		bin = filepath.Join(repoRoot(t), "dist", "bin", "stellad")
	}
	if runtime.GOOS == "windows" && filepath.Ext(bin) != ".exe" {
		bin += ".exe"
	}
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("system: stellad binary not found at %s (run `mise run system-test`, which builds it first): %v", bin, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		t.Fatalf("system: stellad binary %s must be a regular executable file", bin)
	}
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("system: cannot locate source file for repo root")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

func logDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "dist", "logs", "system-test")
	run, present, err := releasecontract.RunFromEnv()
	if err != nil {
		t.Fatalf("system: load release metadata: %v", err)
	}
	if present {
		// Release runs keep every diagnostic below their canonical artifact
		// tree; the short random harness ID still isolates business fixtures.
		dir = filepath.Join(releasecontract.RunDirectory(repoRoot(t), run.ID), "artifacts", "system")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("system: create log dir %s: %v", dir, err)
	}
	return dir
}

// freePort asks the OS for an unused loopback port. The close-and-rebind race
// is contained by startServer's bounded retry.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("system: pick free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// newRunID returns a short random ID that scopes every fixture name of one
// run, so re-runs and shared infrastructure can never collide on business data.
func newRunID(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("system: run id: %v", err)
	}
	return hex.EncodeToString(b[:])
}
