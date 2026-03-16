package main

import (
	"os/signal"
	"syscall"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/channel"
	clicmd "github.com/vaayne/anna/internal/channel/cli"
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
			defer func() { _ = s.pluginMgr.Close() }()

			if s.schedulerSvc != nil {
				if err := s.schedulerSvc.Start(s.ctx); err != nil {
					return err
				}
				defer func() { _ = s.schedulerSvc.Stop() }()
			}

			if c.Bool("stream") {
				return clicmd.RunStream(s.ctx, s.pool)
			}
			listFn := func() []channel.ModelOption {
				return collectModelsFromStore(s.ctx, s.store, s.snap)
			}
			switchFn := modelSwitcher(s.snap, s.store, s.pool, s.extraTools, s.pluginMgr.Registry())
			return clicmd.RunChat(s.ctx, s.pool, s.snap.Provider, s.snap.Model, listFn, switchFn)
		},
	}
}
