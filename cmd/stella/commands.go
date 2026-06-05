package main

import (
	"fmt"

	ucli "github.com/urfave/cli/v2"
)

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
			movedCommand("server", "stellad server"),
			movedCommand("service", "stellad service"),
			movedCommand("upgrade", "stellad upgrade"),
			movedCommand("auth", "stellad auth"),
		},
	}
}

// movedCommand creates a hidden shim for commands that moved to stellad,
// guiding users who still have muscle memory or scripts from before the split.
func movedCommand(name, replacement string) *ucli.Command {
	return &ucli.Command{
		Name:   name,
		Hidden: true,
		Action: func(c *ucli.Context) error {
			return fmt.Errorf(
				"%q has moved to the stellad binary.\n\n"+
					"Run: %s\n\n"+
					"If stellad is not installed, upgrade with:\n"+
					"  brew upgrade stella    (Homebrew)\n"+
					"  or download both binaries from the latest GitHub release",
				"stella "+name, replacement,
			)
		},
	}
}
