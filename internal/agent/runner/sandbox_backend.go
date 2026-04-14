package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/plugins/sandbox/boxsh/boxshclient"
)

// runnerSession wraps a sandbox.Session for runner use.
type runnerSession struct {
	session     sandbox.Session
	policy      sandbox.Policy
	alwaysAlive bool
}

// SessionDir returns the session workspace directory.
func (r *runnerSession) SessionDir() string {
	if r == nil || r.session == nil {
		return ""
	}
	resolved, err := r.session.Host().ResolvePath(string(os.PathSeparator))
	if err != nil {
		return ""
	}
	return resolved
}

func (r *runnerSession) Host() sandbox.Host {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session.Host()
}

// Session returns the underlying sandbox session.
func (r *runnerSession) Session() sandbox.Session {
	if r == nil {
		return nil
	}
	return r.session
}

// Policy returns the session's effective policy.
func (r *runnerSession) Policy() sandbox.Policy {
	if r == nil {
		return sandbox.Policy{}
	}
	return r.policy
}

// Alive reports whether the session is healthy.
func (r *runnerSession) Alive() bool {
	if r == nil {
		return false
	}
	if r.session == nil {
		return r.alwaysAlive
	}
	return r.session.Alive()
}

// Done returns a channel that closes when the session terminates.
func (r *runnerSession) Done() <-chan struct{} {
	if r == nil || r.session == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return r.session.Done()
}

// Close shuts down the session.
func (r *runnerSession) Close() error {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session.Close()
}

// sessionFactory creates a runnerSession from configuration.
type sessionFactory func(context.Context, GoRunnerConfig) (*runnerSession, error)

// registry manages session factories by name.
var sessionRegistry = map[string]sessionFactory{
	config.SandboxBackendLocal: createLocalSession,
	config.SandboxBackendBoxsh: createBoxshSession,
}

var platformSupportsBoxsh = boxshclient.PlatformSupportsBoxsh

func createLocalSession(_ context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
	paths := resolveRunnerPaths(cfg)
	policy := sandbox.Policy{
		Backend: config.SandboxBackendLocal,
		Relaxed: true,
		Filesystem: sandbox.FilesystemPolicy{
			WorkspaceRoot: paths.SandboxRoot,
			WorkingDir:    paths.WorkDir,
			AllowEscapes:  false,
		},
		Network: sandbox.NetworkPolicy{
			Mode: sandbox.NetworkAllowAll,
		},
		Process: sandbox.ProcessPolicy{
			Environment: sandboxProcessEnv(paths),
			InheritEnv:  true,
		},
	}

	factory := sandbox.GlobalRegistry().Get(config.SandboxBackendLocal)
	if factory == nil {
		return nil, fmt.Errorf("local factory not available")
	}

	session, err := factory.CreateSession(context.Background(), policy)
	if err != nil {
		return nil, fmt.Errorf("create local session: %w", err)
	}

	return &runnerSession{
		session: session,
		policy:  policy,
	}, nil
}

func createBoxshSession(ctx context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
	if !platformSupportsBoxsh() {
		return nil, fmt.Errorf("sandbox backend %q is not supported on %s", config.SandboxBackendBoxsh, runtime.GOOS)
	}

	paths := resolveRunnerPaths(cfg)
	readOnlyDirs := collectSandboxReadOnlyDirs(
		paths.ToolsBinDir,
		os.Getenv("PATH"),
	)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		readOnlyDirs = append(readOnlyDirs,
			filepath.Join(home, ".agents", "skills"),
			filepath.Join(home, ".agents", "agents"),
		)
	}
	readOnlyDirs = append(readOnlyDirs, paths.BuiltinSkillsDir)

	policy := sandbox.Policy{
		Backend: config.SandboxBackendBoxsh,
		Relaxed: false,
		Filesystem: sandbox.FilesystemPolicy{
			WorkspaceRoot: paths.SandboxRoot,
			WorkingDir:    paths.WorkDir,
			ReadOnlyPaths: readOnlyDirs,
			AllowEscapes:  false,
		},
		Network: sandbox.NetworkPolicy{
			Mode:      sandbox.NetworkMode(cfg.Sandbox.Network.Mode),
			Allowlist: cfg.Sandbox.Network.Allowlist,
		},
		Process: sandbox.ProcessPolicy{
			Environment: sandboxProcessEnv(paths),
			InheritEnv:  true,
		},
	}

	factory := sandbox.GlobalRegistry().Get(config.SandboxBackendBoxsh)
	if factory == nil {
		return nil, fmt.Errorf("boxsh factory not available")
	}

	session, err := createSessionWithAnnaHome(ctx, factory, policy, paths.AnnaHome)
	if err != nil {
		return nil, fmt.Errorf("create boxsh session: %w", err)
	}

	return &runnerSession{
		session: session,
		policy:  policy,
	}, nil
}

var annaHomeEnvMu = struct{ ch chan struct{} }{ch: make(chan struct{}, 1)}

func createSessionWithAnnaHome(ctx context.Context, factory sandbox.Factory, policy sandbox.Policy, annaHome string) (sandbox.Session, error) {
	if annaHome == "" {
		return factory.CreateSession(ctx, policy)
	}

	annaHomeEnvMu.ch <- struct{}{}
	defer func() { <-annaHomeEnvMu.ch }()

	previous, hadPrevious := os.LookupEnv("ANNA_HOME")
	if err := os.Setenv("ANNA_HOME", annaHome); err != nil {
		return nil, err
	}
	config.ResetAnnaHome()
	defer func() {
		if hadPrevious {
			_ = os.Setenv("ANNA_HOME", previous)
		} else {
			_ = os.Unsetenv("ANNA_HOME")
		}
		config.ResetAnnaHome()
	}()

	return factory.CreateSession(ctx, policy)
}

// resolveSession creates a runnerSession from configuration.
// Replaces the old resolveSandboxBackend which leaked boxsh types.
func resolveSession(ctx context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
	name := resolveSessionBackendName(cfg.Sandbox)

	factory, ok := sessionRegistry[name]
	if !ok {
		return nil, fmt.Errorf("unknown sandbox backend: %q", name)
	}
	return factory(ctx, cfg)
}

func resolveSessionBackendName(cfg config.SandboxConfig) string {
	name := cfg.BackendName()
	if name != config.SandboxBackendAuto {
		return name
	}
	if platformSupportsBoxsh() {
		return config.SandboxBackendBoxsh
	}
	return config.SandboxBackendLocal
}
