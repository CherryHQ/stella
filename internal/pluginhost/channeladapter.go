package pluginhost

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/vaayne/anna/internal/pluginapi"
)

// ChannelAdapter exposes a channel plugin as a restartable host-side runner.
type ChannelAdapter struct {
	def    Definition
	logger *slog.Logger

	supervisor *Supervisor
	mu         sync.Mutex
	stopped    bool
}

func NewChannelAdapter(def Definition, opts SupervisorOptions) *ChannelAdapter {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &ChannelAdapter{
		def:        def,
		logger:     logger.With("plugin", def.ID()),
		supervisor: NewSupervisor(def, opts),
	}
}

func (a *ChannelAdapter) Name() string { return a.def.Manifest.Name }

func (a *ChannelAdapter) Start(ctx context.Context) error {
	client, err := a.supervisor.Start(ctx)
	if err != nil {
		return err
	}
	waitCh := make(chan error, 1)
	go func(c *Client) { waitCh <- c.Wait() }(client)

	for {
		select {
		case <-ctx.Done():
			a.markStopped()
			_ = a.supervisor.Close()
			return ctx.Err()
		case err := <-waitCh:
			if ctx.Err() != nil || a.isStopped() {
				return ctx.Err()
			}
			if err != nil {
				a.logger.Warn("channel plugin exited, restarting", "error", err)
			} else {
				a.logger.Warn("channel plugin exited, restarting")
			}
			client, err = a.supervisor.Restart(ctx)
			if err != nil {
				return fmt.Errorf("restart channel plugin: %w", err)
			}
			waitCh = make(chan error, 1)
			go func(c *Client) { waitCh <- c.Wait() }(client)
		}
	}
}

func (a *ChannelAdapter) Stop() {
	a.markStopped()
	_ = a.supervisor.Close()
}

func (a *ChannelAdapter) Notify(ctx context.Context, n pluginapi.ChannelNotification) error {
	client, err := a.supervisor.EnsureHealthy(ctx)
	if err != nil {
		return err
	}

	var resp pluginapi.ChannelNotifyResponse
	if err := client.Request(ctx, "notify", pluginapi.ChannelNotifyRequest{Notification: n}, &resp); err != nil {
		return err
	}
	if !resp.Delivered {
		if resp.Error != "" {
			return fmt.Errorf("%s", resp.Error)
		}
		return fmt.Errorf("channel notification not delivered")
	}
	return nil
}

func (a *ChannelAdapter) markStopped() {
	a.mu.Lock()
	a.stopped = true
	a.mu.Unlock()
}

func (a *ChannelAdapter) isStopped() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped
}
