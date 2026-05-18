package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// Pool manages a set of sessions, each with its own history and
// It is the only type channels interact with.
type Pool struct {
	agentID          string // agent this pool belongs to (empty for legacy single-agent)
	factory          NewRunnerFunc
	hooksFn          func() []hooks.HookPlugin // injected into RunnerParams; nil = no hooks
	beforeRunFn      BeforeRunBuilder
	snapshotPromptFn func(ctx context.Context, userID string, agentID string, snap memory.SessionSnapshot) string
	sessions         map[string]*Session
	mem              memory.Provider // memory provider — sole persistence layer
	mu               sync.Mutex
	idleTimeout      time.Duration
	compaction       CompactionConfig
	defaultModel     string // default model ID for new runners
	fastModel        string // model ID used for compaction / fast tasks
	log              *slog.Logger
}

// NewPool creates a new Pool with the given runner factory and memory provider.
// The memory provider is required — it is the sole persistence layer for sessions.
func NewPool(factory NewRunnerFunc, mem memory.Provider, opts ...PoolOption) *Pool {
	p := &Pool{
		factory:     factory,
		mem:         mem,
		sessions:    make(map[string]*Session),
		idleTimeout: 10 * time.Minute,
		log:         slog.With("component", "pool"),
	}
	for _, opt := range opts {
		opt(p)
	}
	// Enrich logger with agent ID when set.
	if p.agentID != "" {
		p.log = p.log.With("agent_id", p.agentID)
	}
	return p
}

// AgentID returns the agent ID this pool belongs to.
func (p *Pool) AgentID() string {
	return p.agentID
}

// SetFactory replaces the runner factory used for new runners.
// Existing runners are not affected until their session is reset.
func (p *Pool) SetFactory(factory NewRunnerFunc) {
	p.mu.Lock()
	p.factory = factory
	p.mu.Unlock()
}

// Factory returns the current runner factory.
func (p *Pool) Factory() NewRunnerFunc {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.factory
}

// hookPlugins returns the current hook plugins, or nil if none configured.
func (p *Pool) hookPlugins() []hooks.HookPlugin {
	p.mu.Lock()
	fn := p.hooksFn
	p.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// SetHooks updates the hook getter used when creating new runners.
// Changing hooks never requires rebuilding the factory or resetting sessions —
// new runners created after this call will use the updated hooks.
func (p *Pool) SetHooks(fn func() []hooks.HookPlugin) {
	p.mu.Lock()
	p.hooksFn = fn
	p.mu.Unlock()
}

// SetDefaultModel updates the default model used for new runners.
// Call this alongside SetFactory when the user switches models at runtime.
func (p *Pool) SetDefaultModel(model string) {
	p.mu.Lock()
	p.defaultModel = model
	p.mu.Unlock()
}

// SetFastModel updates the fast model used for compaction and utility work.
func (p *Pool) SetFastModel(model string) {
	p.mu.Lock()
	p.fastModel = model
	p.mu.Unlock()
}

// ResetRunners closes all live session runners but keeps session metadata and history.
// The next chat on each session will recreate a runner from the current factory.
func (p *Pool) ResetRunners() error {
	p.mu.Lock()
	runners := make([]Runner, 0, len(p.sessions))
	for _, sess := range p.sessions {
		if sess == nil {
			continue
		}
		if sess.Runner != nil {
			runners = append(runners, sess.Runner)
			sess.Runner = nil
		}
		sess.Model = ""
	}
	p.mu.Unlock()

	var lastErr error
	for _, r := range runners {
		if err := r.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// ResetRunnersForUser closes live runners belonging to a specific user.
// Other sessions are not affected. The next chat for that user recreates a
func (p *Pool) ResetRunnersForUser(userID string) error {
	p.mu.Lock()
	var runners []Runner
	for _, sess := range p.sessions {
		if sess == nil || sess.Info.UserID != userID {
			continue
		}
		if sess.Runner != nil {
			runners = append(runners, sess.Runner)
			sess.Runner = nil
		}
		sess.Model = ""
	}
	p.mu.Unlock()

	var lastErr error
	for _, r := range runners {
		if err := r.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// Close shuts down all sessions and runners.
func (p *Pool) Close() error {
	return p.close(true)
}

func (p *Pool) close(closeMemory bool) error {
	p.mu.Lock()
	sessions := p.sessions
	p.sessions = make(map[string]*Session)
	p.mu.Unlock()

	var lastErr error
	for id, sess := range sessions {
		p.log.Info("closing session", "session_id", id)
		if sess.Runner != nil {
			if err := sess.Runner.Close(); err != nil {
				lastErr = err
			}
		}
	}

	if closeMemory && p.mem != nil {
		if err := p.mem.Close(); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

func (p *Pool) closeSessions() error {
	return p.close(false)
}
