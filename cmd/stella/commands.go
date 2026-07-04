package main

import (
	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/version"
)

func newApp() *ucli.App {
	return &ucli.App{
		Name:  "stella",
		Usage: "A local AI assistant CLI",
		Description: `Stella CLI provides commands to interact with a running stella server.
Use these commands to manage goals, schedules, content, secrets, and more.
Start the server with "stellad server".`,
		Version: version.DisplayVersion(),
		Commands: []*ucli.Command{
			versionCommand(),
			miseCommand(),
			recallyCommand(),
			schedulerCommand(),
			goalCommand(),
			workflowCommand(),
			emailCommand(),
			vaultCommand(),
			tokenCommand(),
			mcpCommand(),
			oauthCommand(),
			oauthServerCommand(),
			shareCommand(),
			dbCommand(),
		},
	}
}
