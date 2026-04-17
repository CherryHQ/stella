package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

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
	ctx, span := sandboxTracer.Start(ctx, "sandbox.create_session",
		trace.WithAttributes(
			attribute.String("anna.sandbox.backend", config.SandboxBackendBoxsh),
			attribute.String("anna.sandbox.agent_root", cfg.AgentRoot),
			attribute.String("anna.sandbox.user_root", cfg.UserRoot),
			attribute.String("anna.sandbox.project_root", cfg.ProjectRoot),
		),
	)
	defer span.End()

	if !platformSupportsBoxsh() {
		err := fmt.Errorf("sandbox backend %q is not supported on %s", config.SandboxBackendBoxsh, runtime.GOOS)
		recordSandboxError(span, err)
		return nil, err
	}

	runnerPaths := resolveRunnerPaths(cfg)
	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		err = fmt.Errorf("resolve sandbox paths: %w", err)
		recordSandboxError(span, err)
		return nil, err
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

	span.SetAttributes(
		attribute.String("anna.sandbox.resolved_user_root", paths.UserRoot),
		attribute.String("anna.sandbox.work_dir", paths.WorkDir),
		attribute.Int("anna.sandbox.readonly_dir_count", len(readOnlyDirs)),
		attribute.String("anna.sandbox.network.mode", cfg.Sandbox.Network.Mode),
	)
	if len(cfg.Sandbox.Network.Allowlist) > 0 {
		span.SetAttributes(attribute.StringSlice("anna.sandbox.network.allowlist", cfg.Sandbox.Network.Allowlist))
	}

	slog.Info("creating boxsh session",
		"component", "runner_sandbox",
		"user_root", paths.UserRoot,
		"work_dir", paths.WorkDir,
		"readonly_dirs", readOnlyDirs,
		"network_mode", cfg.Sandbox.Network.Mode,
		"network_allowlist", cfg.Sandbox.Network.Allowlist,
	)

	factory := sandbox.GlobalRegistry().Get(config.SandboxBackendBoxsh)
	if factory == nil {
		err := fmt.Errorf("boxsh factory not available")
		recordSandboxError(span, err)
		return nil, err
	}

	session, err := factory.CreateSession(ctx, policy)
	if err != nil {
		err = fmt.Errorf("create boxsh session: %w", err)
		recordSandboxError(span, err)
		return nil, err
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
	candidates := []string{
		paths.annaSkillsDir(),
		paths.annaAgentsDir(),
		paths.agentSkillsDir(),
		paths.agentAgentsDir(),
		paths.projectSkillsDir(),
		paths.projectAgentsDir(),
	}

	readOnly := make([]string, 0, len(candidates))
	for _, dir := range candidates {
		if dir == "" || isWithinPathRoot(paths.UserRoot, dir) {
			continue
		}
		readOnly = append(readOnly, dir)
	}
	return readOnly
}

func isWithinPathRoot(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
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
