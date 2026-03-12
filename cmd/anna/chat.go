package main

import (
	"os/signal"
	"syscall"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/channel"
	clicmd "github.com/vaayne/anna/channel/cli"
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
			defer func() { _ = s.pool.Close() }()

			if s.cronSvc != nil {
				if err := s.cronSvc.Start(s.ctx); err != nil {
					return err
				}
				defer func() { _ = s.cronSvc.Stop() }()
			}

			if c.Bool("stream") {
				return clicmd.RunStream(s.ctx, s.pool)
			}
			listFn := func() []channel.ModelOption { return collectModels(s.cfg) }
			switchFn := modelSwitcher(s.cfg, s.pool, s.extraTools)
			return clicmd.RunChat(s.ctx, s.pool, s.cfg.Provider, s.cfg.Model, listFn, switchFn)
		},
	}
}
