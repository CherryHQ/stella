package main

import ucli "github.com/urfave/cli/v2"

func newApp() *ucli.App {
	return &ucli.App{
		Name:  "stella",
		Usage: "A local AI assistant CLI",
		Description: `Stella CLI provides commands to interact with a running stella server.
Use these commands to manage tasks, schedules, content, secrets, and more.
Start the server with "stellad server".`,
		Version: displayVersion(),
		Commands: []*ucli.Command{
			skillsCommand(),
			versionCommand(),
			recallyCommand(),
			schedulerCommand(),
			emailCommand(),
			vaultCommand(),
			oauthCommand(),
			shareCommand(),
			taskCommand(),
		},
	}
}
