package runner

import (
	"context"
	"fmt"
	"os"
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

func runnerFilesystemPolicy(paths sandboxPaths, readOnlyPaths []string) sandbox.FilesystemPolicy {
	return sandbox.FilesystemPolicy{
		WorkspaceRoot: paths.UserRoot,
		WorkingDir:    paths.WorkDir,
		ReadOnlyPaths: readOnlyPaths,
		AllowEscapes:  false,
	}
}

func createLocalSession(_ context.Context, cfg GoRunnerConfig) (*runnerSession, error) {
	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox paths: %w", err)
	}
	policy := sandbox.Policy{
		Backend:    config.SandboxBackendLocal,
		Relaxed:    true,
		Filesystem: runnerFilesystemPolicy(paths, nil),
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

	runnerPaths := resolveRunnerPaths(cfg)
	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox paths: %w", err)
	}
	readOnlyDirs := collectSandboxReadOnlyDirs(
		sandboxReadableDirs(runnerPaths),
		runnerPaths.toolsBinDir(),
		os.Getenv("PATH"),
	)

	policy := sandbox.Policy{
		Backend:    config.SandboxBackendBoxsh,
		Relaxed:    false,
		Filesystem: runnerFilesystemPolicy(paths, readOnlyDirs),
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

	session, err := factory.CreateSession(ctx, policy)
	if err != nil {
		return nil, fmt.Errorf("create boxsh session: %w", err)
	}

	return &runnerSession{
		session: session,
		policy:  policy,
	}, nil
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

func sandboxReadableDirs(paths runnerPaths) []string {
	return []string{
		paths.builtinSkillsDir(),
		paths.annaSkillsDir(),
		paths.annaAgentsDir(),
		paths.agentSkillsDir(),
		paths.agentAgentsDir(),
		paths.projectSkillsDir(),
		paths.projectAgentsDir(),
	}
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
