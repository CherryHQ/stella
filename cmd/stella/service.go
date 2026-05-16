package main

import (
	ucli "github.com/urfave/cli/v2"
)

type serviceManager interface {
	Install(system bool) error
	Uninstall(system bool) error
	Start() error
	Stop() error
	Restart() error
	Status() error
	Logs(follow bool) error
}

func serviceCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "service",
		Usage:    "Manage the stella background service",
		Category: "System",
		Description: `Install, start, stop, and monitor stella as a background service
(launchd on macOS, systemd on Linux). Logs can be streamed with
"service logs --follow".`,
		Subcommands: []*ucli.Command{
			{
				Name:  "install",
				Usage: "Install and enable stella as a background service",
				Flags: []ucli.Flag{
					&ucli.BoolFlag{
						Name:  "system",
						Usage: "Install as a system-wide service (Linux only, requires root)",
					},
				},
				Action: func(c *ucli.Context) error {
					return newServiceManager().Install(c.Bool("system"))
				},
			},
			{
				Name:  "uninstall",
				Usage: "Disable and remove the stella background service",
				Flags: []ucli.Flag{
					&ucli.BoolFlag{
						Name:  "system",
						Usage: "Uninstall the system-wide service (Linux only, requires root)",
					},
				},
				Action: func(c *ucli.Context) error {
					return newServiceManager().Uninstall(c.Bool("system"))
				},
			},
			{
				Name:  "start",
				Usage: "Start the stella service",
				Action: func(c *ucli.Context) error {
					return newServiceManager().Start()
				},
			},
			{
				Name:  "stop",
				Usage: "Stop the stella service",
				Action: func(c *ucli.Context) error {
					return newServiceManager().Stop()
				},
			},
			{
				Name:  "restart",
				Usage: "Restart the stella service",
				Action: func(c *ucli.Context) error {
					return newServiceManager().Restart()
				},
			},
			{
				Name:  "status",
				Usage: "Show the status of the stella service",
				Action: func(c *ucli.Context) error {
					return newServiceManager().Status()
				},
			},
			{
				Name:  "logs",
				Usage: "Show stella service logs",
				Flags: []ucli.Flag{
					&ucli.BoolFlag{
						Name:    "follow",
						Aliases: []string{"f"},
						Usage:   "Follow log output",
					},
				},
				Action: func(c *ucli.Context) error {
					return newServiceManager().Logs(c.Bool("follow"))
				},
			},
		},
	}
}
