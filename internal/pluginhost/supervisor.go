package pluginhost

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Supervisor struct {
	def          Definition
	logger       *slog.Logger
	restartDelay time.Duration

	mu       sync.Mutex
	client   *Client
	restarts int
}

type SupervisorOptions struct {
	Logger       *slog.Logger
	RestartDelay time.Duration
}

func NewSupervisor(def Definition, opts SupervisorOptions) *Supervisor {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	delay := opts.RestartDelay
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	return &Supervisor{
		def:          def,
		logger:       logger.With("plugin", def.ID()),
		restartDelay: delay,
	}
}

func (s *Supervisor) Start(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil && s.client.Alive() {
		return s.client, nil
	}

	client, err := Start(ctx, s.def, StartOptions{Logger: s.logger})
	if err != nil {
		return nil, err
	}
	s.client = client
	return client, nil
}

func (s *Supervisor) EnsureHealthy(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()

	if client == nil || !client.Alive() {
		return s.Restart(ctx)
	}
	if err := client.Health(ctx); err != nil {
		return s.Restart(ctx)
	}
	return client, nil
}

func (s *Supervisor) Restart(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	old := s.client
	s.client = nil
	s.restarts++
	delay := s.restartDelay
	s.mu.Unlock()

	if old != nil {
		_ = old.Close()
	}
	time.Sleep(delay)
	return s.Start(ctx)
}

func (s *Supervisor) Close() error {
	s.mu.Lock()
	client := s.client
	s.client = nil
	s.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Close()
}

func (s *Supervisor) RestartCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}
