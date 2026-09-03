package testbed

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/test/fakeanthropic"
)

// Options controls one disposable Stella instance. Port zero asks the kernel
// for a free loopback port, which is useful for system tests.
type Options struct {
	RepoRoot  string
	Port      int
	FakeModel bool
	Bootstrap bool
	Managed   bool
	// VaultKey is test-only injection for startup-failure coverage. Empty uses a generated identity.
	VaultKey     string
	OmitVaultKey bool
}

// Instance owns the stellad process, its embedded database, temporary home,
// credentials, and logs. Stop is graceful; Kill is process-group force kill.
func binaryPath(repoRoot string) string { return filepath.Join(repoRoot, "dist", "bin", "stellad") }

type Instance struct {
	repoRoot        string
	port            int
	baseURL         string
	home            string
	dsn             string
	vaultKey        string
	credentialsPath string
	logPath         string
	db              *appdb.Embedded
	cmd             *exec.Cmd
	done            chan struct{}
	waitErr         error
	fake            *fakeanthropic.Fake
	modelServer     *httptest.Server
	modelURL        string
	root            string
	managed         bool
	stateFile       string
	state           supervisorState
}

// Start boots an isolated testbed and waits until stellad is ready and fixture
// credentials have been created.
func Start(ctx context.Context, opts Options) (*Instance, error) {
	if opts.RepoRoot == "" {
		opts.RepoRoot, _ = os.Getwd()
	}
	if opts.Port == 0 {
		opts.Port = freePort()
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return nil, fmt.Errorf("testbed port %d is invalid", opts.Port)
	}
	if _, err := os.Stat(binaryPath(opts.RepoRoot)); err != nil {
		return nil, fmt.Errorf("%s is missing (run `mise run build`)", binaryPath(opts.RepoRoot))
	}
	if err := ensurePortAvailable(opts.Port); err != nil {
		return nil, err
	}

	root, err := os.MkdirTemp("", "stella-testbed-")
	if err != nil {
		return nil, fmt.Errorf("create temporary test root: %w", err)
	}
	instance := &Instance{repoRoot: opts.RepoRoot, port: opts.Port, root: root, done: make(chan struct{}), managed: opts.Managed}
	if opts.FakeModel {
		instance.fake = fakeanthropic.New()
		chunks, _ := strconv.Atoi(os.Getenv("PERF_STREAM_CHUNKS"))
		interval, _ := strconv.Atoi(os.Getenv("PERF_STREAM_INTERVAL_MS"))
		instance.modelServer = httptest.NewServer(fakeanthropic.MessageHandlerWithOptions(fakeanthropic.MessageHandlerOptions{StreamChunks: chunks, StreamIntervalMS: interval}))
		instance.modelURL = instance.modelServer.URL
	}
	cleanup := func() {
		_ = instance.Kill()
		if instance.fake != nil {
			instance.fake.Close()
			instance.fake = nil
		}
		if instance.modelServer != nil {
			instance.modelServer.Close()
			instance.modelServer = nil
		}
		if instance.db != nil {
			_ = instance.db.Stop()
		}
		if instance.stateFile != "" {
			_ = removeOwnedState(instance.stateFile, instance.state)
		}
		_ = os.RemoveAll(root)
	}
	if opts.Managed {
		owner, err := currentIdentity()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("identify testbed supervisor: %w", err)
		}
		instanceID, err := randomID()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("generate testbed identity: %w", err)
		}
		instance.stateFile = statePath(opts.RepoRoot)
		instance.state = supervisorState{Version: stateVersion, Owner: owner, Instance: instanceID, Root: root}
		if err := claimOrRecover(instance.stateFile, instance.state); err != nil {
			cleanup()
			return nil, err
		}
	}
	instance.home = filepath.Join(root, "home")
	if err := os.MkdirAll(instance.home, 0o700); err != nil {
		cleanup()
		return nil, err
	}
	instance.db, err = appdb.StartEmbedded("", 0)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("start embedded postgres: %w", err)
	}
	instance.dsn = instance.db.DSN()
	instance.vaultKey = opts.VaultKey
	if instance.vaultKey == "" {
		instance.vaultKey, err = vault.GenerateMasterIdentity()
		if err != nil {
			cleanup()
			return nil, err
		}
	}
	instance.baseURL = fmt.Sprintf("http://127.0.0.1:%d", opts.Port)
	instance.logPath = filepath.Join(opts.RepoRoot, "dist", "logs", "testbed", fmt.Sprintf("server-%d.log", opts.Port))
	if err := os.MkdirAll(filepath.Dir(instance.logPath), 0o755); err != nil {
		cleanup()
		return nil, err
	}
	logFile, err := os.OpenFile(instance.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		cleanup()
		return nil, err
	}
	instance.cmd = exec.Command(binaryPath(opts.RepoRoot), "server")
	instance.cmd.Dir, instance.cmd.Stdout, instance.cmd.Stderr = opts.RepoRoot, logFile, logFile
	instance.cmd.Env = serverEnvironment(instance.home, instance.dsn, instance.vaultKey, opts.Port)
	if opts.OmitVaultKey {
		filtered := instance.cmd.Env[:0]
		for _, value := range instance.cmd.Env {
			if !strings.HasPrefix(value, "STELLA_VAULT_KEY=") {
				filtered = append(filtered, value)
			}
		}
		instance.cmd.Env = filtered
	}
	setProcessGroup(instance.cmd)
	if err := instance.cmd.Start(); err != nil {
		_ = logFile.Close()
		cleanup()
		return nil, fmt.Errorf("start stellad: %w", err)
	}
	_ = logFile.Close()
	go func() { instance.waitErr = instance.cmd.Wait(); close(instance.done) }()
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := waitServerReady(readyCtx, instance.baseURL, instance.done); err != nil {
		cleanup()
		return nil, fmt.Errorf("%w\nserver log: %s\n%s", err, instance.logPath, instance.LogTail(40))
	}
	if opts.Bootstrap {
		if _, _, err := bootstrap(ctx, bootstrapConfig{BaseURL: instance.baseURL, Home: instance.home, DatabaseURL: instance.dsn}); err != nil {
			cleanup()
			return nil, fmt.Errorf("bootstrap test identities: %w", err)
		}
		instance.credentialsPath = filepath.Join(instance.home, credentialsFilename)
	}
	if instance.fake != nil && opts.Bootstrap {
		creds, err := instance.Credentials()
		if err != nil {
			cleanup()
			return nil, err
		}
		creds.FakeModel.ProviderID = "testbed-fake-anthropic"
		creds.FakeModel.BaseURL = instance.modelURL
		if err := updateCredentials(instance.credentialsPath, creds); err != nil {
			cleanup()
			return nil, err
		}
		provider := map[string]any{
			"id": "testbed-fake-anthropic", "type": "anthropic", "name": "Testbed Fake Anthropic", "enabled": true,
			"api_key": "testbed-fake-model", "base_url": instance.modelURL,
			"models": map[string]any{"claude-sonnet-4-6": map[string]any{"id": "claude-sonnet-4-6", "enabled": true, "input": []string{"text", "image"}}},
		}
		if err := postJSONWithBearer(ctx, &http.Client{}, instance.baseURL, "/api/providers", creds.Admin.Token, provider, http.StatusCreated, nil); err != nil {
			cleanup()
			return nil, fmt.Errorf("register fake model provider: %w", err)
		}
	}
	return instance, nil
}

func ensurePortAvailable(port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("testbed port %d is already in use: %w", port, err)
	}
	return listener.Close()
}

func freePort() int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}
func (i *Instance) BaseURL() string     { return i.baseURL }
func (i *Instance) DatabaseURL() string { return i.dsn }
func (i *Instance) Credentials() (Credentials, error) {
	return loadCredentialsPublic(i.credentialsPath)
}
func (i *Instance) Fake() *fakeanthropic.Fake { return i.fake }
func (i *Instance) LogPath() string           { return i.logPath }
func (i *Instance) LogTail(n int) string {
	data, err := os.ReadFile(i.logPath)
	if err != nil {
		return fmt.Sprintf("(read server log: %v)", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "server log tail:\n" + strings.Join(lines, "\n")
}
func (i *Instance) Done() <-chan struct{} { return i.done }
func (i *Instance) WaitErr() error        { return i.waitErr }
func (i *Instance) Terminate() error {
	if i.cmd == nil || i.cmd.Process == nil {
		return errors.New("stellad is not running")
	}
	return terminateProcess(i.cmd.Process)
}
func (i *Instance) ProcessGroupAlive() bool { return processGroupAlive(i.cmd) }

func (i *Instance) Stop() error {
	if i.cmd != nil {
		stopServer(i.cmd, i.done)
	}
	if i.fake != nil {
		i.fake.Close()
		i.fake = nil
	}
	if i.modelServer != nil {
		i.modelServer.Close()
		i.modelServer = nil
	}
	if i.db != nil {
		if err := i.db.Stop(); err != nil {
			return err
		}
		i.db = nil
	}
	if i.stateFile != "" {
		_ = removeOwnedState(i.stateFile, i.state)
	}
	return os.RemoveAll(i.root)
}

func (i *Instance) Kill() error {
	if i.cmd == nil || i.cmd.Process == nil {
		return nil
	}
	killProcessGroup(i.cmd)
	select {
	case <-i.done:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("stellad did not exit after process-group kill")
	}
}

func (i *Instance) Restart(ctx context.Context) error {
	if err := i.Kill(); err != nil {
		return err
	}
	i.cmd = exec.Command(binaryPath(i.repoRoot), "server")
	i.cmd.Dir = i.repoRoot
	logFile, err := os.OpenFile(i.logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	i.cmd.Stdout, i.cmd.Stderr = logFile, logFile
	i.cmd.Env = serverEnvironment(i.home, i.dsn, i.vaultKey, i.port)
	setProcessGroup(i.cmd)
	i.done = make(chan struct{})
	if err := i.cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	go func() { i.waitErr = i.cmd.Wait(); close(i.done) }()
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return waitServerReady(readyCtx, i.baseURL, i.done)
}

// Credentials is the public, secret-bearing testbed credential record. Callers
// must keep it in memory and never print it.
type Credentials = credentials

func loadCredentialsPublic(path string) (Credentials, error) {
	c, found, err := loadCredentials(path)
	if err != nil {
		return Credentials{}, err
	}
	if !found {
		return Credentials{}, fmt.Errorf("credentials file %s does not exist", path)
	}
	return c, nil
}
