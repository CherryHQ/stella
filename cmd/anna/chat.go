package main

import (
	"fmt"
	"os/signal"
	"syscall"

	ucli "github.com/urfave/cli/v2"
	clicmd "github.com/vaayne/anna/internal/chatcli"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

func chatCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "chat",
		Usage: "Start interactive CLI chat",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{
				Name:  "stream",
				Usage: "Read prompt from stdin and stream response to stdout",
			},
			&ucli.StringFlag{
				Name:  "agent",
				Usage: "Agent to chat with (default: first enabled agent)",
			},
		},
		Action: func(c *ucli.Context) error {
			if !c.Bool("stream") {
				if err := setupLogFile(); err != nil {
					return err
				}
			}

			ctx, cancel := signal.NotifyContext(c.Context, syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			s, err := setup(ctx, false)
			if err != nil {
				return err
			}
			defer func() { _ = s.poolManager.Close() }()

			// Resolve the target agent's pool.
			pool := s.pool
			snap := s.snap
			if agentFlag := c.String("agent"); agentFlag != "" {
				p := s.poolManager.Get(agentFlag)
				if p == nil {
					return fmt.Errorf("agent %q not found or not enabled", agentFlag)
				}
				pool = p
				// Get a snapshot for the selected agent.
				agentSnap, snapErr := s.store.Snapshot(ctx, agentFlag)
				if snapErr != nil {
					return fmt.Errorf("load config for agent %q: %w", agentFlag, snapErr)
				}
				snap = agentSnap
			}

			if s.schedulerSvc != nil {
				if err := s.schedulerSvc.Start(s.ctx); err != nil {
					return err
				}
				defer func() { _ = s.schedulerSvc.Stop() }()
			}

			if c.Bool("stream") {
				return clicmd.RunStream(s.ctx, pool, s.cliUserID)
			}
			listFn := func() []pkgchannel.ModelOption {
				return collectModelsFromStore(s.ctx, s.store, snap)
			}
			switchFn := modelSwitcher(
				snap,
				s.store,
				pool,
				s.extraTools,
				func(bc plugintools.BuildContext) []tools.Tool {
					return s.pluginHost.BuildCoreTools(bc)
				},
				func(api, apiKey, baseURL string) (*providers.Registry, error) {
					return s.pluginHost.BuildProviderRegistry(api, map[string]any{
						"api_key":  apiKey,
						"base_url": baseURL,
					})
				},
			)
			return clicmd.RunChat(s.ctx, pool, snap.Provider, snap.Model, listFn, switchFn, s.cliUserID)
		},
	}
}
