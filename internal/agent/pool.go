package agent

import (
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/memory"
)

// Pool manages a set of sessions, each with its own history and runner.
// It is the only type channels interact with.
type Pool struct {
	agentID      string // agent this pool belongs to (empty for legacy single-agent)
	factory      runner.NewRunnerFunc
	sessions     map[string]*Session
	mem          memory.Engine // memory engine — sole persistence layer
	mu           sync.Mutex
	idleTimeout  time.Duration
	compaction   CompactionConfig
	defaultModel string                  // default model ID for new runners
	fastModel    string                  // model ID used for compaction / fast tasks
	userMemory   *memory.UserMemoryStore // optional per-user memory for prompt injection
	log          *slog.Logger
}

// NewPool creates a new Pool with the given runner factory and memory engine.
// The memory engine is required — it is the sole persistence layer for sessions.
func NewPool(factory runner.NewRunnerFunc, mem memory.Engine, opts ...PoolOption) *Pool {
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
func (p *Pool) SetFactory(factory runner.NewRunnerFunc) {
	p.mu.Lock()
	p.factory = factory
	p.mu.Unlock()
}

// SetDefaultModel updates the default model used for new runners.
// Call this alongside SetFactory when the user switches models at runtime.
func (p *Pool) SetDefaultModel(model string) {
	p.mu.Lock()
	p.defaultModel = model
	p.mu.Unlock()
}

// Close shuts down all sessions and runners.
func (p *Pool) Close() error {
	p.mu.Lock()
	sessions := p.sessions
	p.sessions = make(map[string]*Session)
	p.mu.Unlock()

	var lastErr error
	for id, sess := range sessions {
		p.log.Info("closing session", "session_id", id)
		if closer, ok := sess.Runner.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				lastErr = err
			}
		}
	}

	if err := p.mem.Close(); err != nil {
		lastErr = err
	}

	return lastErr
}
