package ml

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// SupervisorConfig configures a managed sidecar process.
type SupervisorConfig struct {
	BinPath    string        // path to the stella-ml binary
	SocketPath string        // unix socket the sidecar listens on / the client dials
	Args       []string      // extra flags (runtime-lib, model, tokenizer, version, ...)
	Env        []string      // extra environment (e.g. library search path); appended to os.Environ
	HealthGate time.Duration // how long to wait for first /healthz ok after a spawn
	MinBackoff time.Duration // restart backoff floor
	MaxBackoff time.Duration // restart backoff ceiling
	StableFor  time.Duration // a process healthy this long resets the backoff
}

func (c *SupervisorConfig) setDefaults() {
	if c.HealthGate <= 0 {
		c.HealthGate = 30 * time.Second
	}
	if c.MinBackoff <= 0 {
		c.MinBackoff = 500 * time.Millisecond
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	if c.StableFor <= 0 {
		c.StableFor = 30 * time.Second
	}
}

// Supervisor owns the sidecar lifecycle: lazy spawn, health-gate, restart-on-crash
// with exponential backoff, and reap when the supervising context is cancelled.
type Supervisor struct {
	cfg    SupervisorConfig
	client *Client
	log    *slog.Logger

	readyOnce sync.Once
	ready     chan struct{} // closed on first healthy

	mu       sync.Mutex
	healthy  bool
	restarts int
}

// NewSupervisor builds a supervisor and its client. Call Run to start managing.
func NewSupervisor(cfg SupervisorConfig, log *slog.Logger) *Supervisor {
	cfg.setDefaults()
	return &Supervisor{
		cfg:    cfg,
		client: NewClient(cfg.SocketPath),
		log:    log,
		ready:  make(chan struct{}),
	}
}

// Client returns the sidecar client. Calls before Ready may fail with a transport
// error until the first spawn becomes healthy.
func (s *Supervisor) Client() *Client { return s.client }

// Run manages the sidecar until ctx is cancelled, then reaps it. It blocks, so
// callers typically run it in a goroutine. Returns nil on clean shutdown.
func (s *Supervisor) Run(ctx context.Context) error {
	backoff := s.cfg.MinBackoff
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		started := time.Now()
		runErr := s.runOnce(ctx)
		select {
		case <-ctx.Done():
			return nil // intentional shutdown; runErr is just the cancellation
		default:
		}

		s.mu.Lock()
		s.healthy = false
		s.restarts++
		n := s.restarts
		s.mu.Unlock()

		// A process that stayed up past StableFor was healthy, not crash-looping;
		// reset the backoff so the next restart is prompt.
		if time.Since(started) >= s.cfg.StableFor {
			backoff = s.cfg.MinBackoff
		}
		s.log.Warn("stella-ml exited, restarting", "err", runErr, "restart", n, "backoff", backoff)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff *= 2
		if backoff > s.cfg.MaxBackoff {
			backoff = s.cfg.MaxBackoff
		}
	}
}

// runOnce spawns one sidecar process, health-gates it, and blocks until it exits.
func (s *Supervisor) runOnce(ctx context.Context) error {
	args := append([]string{"-socket", s.cfg.SocketPath}, s.cfg.Args...)
	cmd := exec.CommandContext(ctx, s.cfg.BinPath, args...)
	cmd.Env = append(os.Environ(), s.cfg.Env...)
	cmd.Stdout = os.Stderr // sidecar logs JSON to stderr; surface both streams there
	cmd.Stderr = os.Stderr
	// On ctx cancel, ask the sidecar to shut down gracefully, then SIGKILL if it
	// overstays. On Linux, also have the kernel kill it if stellad dies (reap).
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	setParentDeathSignal(cmd)

	if err := cmd.Start(); err != nil {
		return err
	}
	s.log.Info("stella-ml started", "pid", cmd.Process.Pid, "socket", s.cfg.SocketPath)

	// Health-gate in the background so a never-healthy process is logged distinctly
	// from a clean run; the restart loop still keys off process exit.
	go s.healthGate(ctx, cmd.Process.Pid)

	return cmd.Wait()
}

// healthGate polls /healthz until the sidecar is ready, the gate times out, or ctx
// is cancelled. On first ready it unblocks Ready() and clears the restart counter.
func (s *Supervisor) healthGate(ctx context.Context, pid int) {
	deadline := time.Now().Add(s.cfg.HealthGate)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hctx, cancel := context.WithTimeout(ctx, time.Second)
			h, err := s.client.Health(hctx)
			cancel()
			if err == nil && h.Ready() {
				s.mu.Lock()
				s.healthy = true
				s.restarts = 0
				s.mu.Unlock()
				s.readyOnce.Do(func() { close(s.ready) })
				s.log.Info("stella-ml healthy", "pid", pid, "runtime_version", h.RuntimeVersion)
				return
			}
			if time.Now().After(deadline) {
				s.log.Error("stella-ml not healthy within gate", "pid", pid, "gate", s.cfg.HealthGate, "last_err", err)
				return
			}
		}
	}
}

// Ready blocks until the sidecar first becomes healthy, or ctx is done.
func (s *Supervisor) Ready(ctx context.Context) error {
	select {
	case <-s.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Healthy reports the last observed health state (non-blocking).
func (s *Supervisor) Healthy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthy
}
