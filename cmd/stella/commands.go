package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

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
			upgradeShimCommand(),
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

// upgradeShimCommand delegates to stellad upgrade if found alongside this
// binary; otherwise prints migration guidance. This handles the transition
// where pre-split users run "stella upgrade" and get the new thin CLI.
func upgradeShimCommand() *ucli.Command {
	return &ucli.Command{
		Name:   "upgrade",
		Hidden: true,
		Action: func(c *ucli.Context) error {
			stellad := findCompanionDaemon()
			if stellad != "" {
				cmd := exec.CommandContext(c.Context, stellad, append([]string{"upgrade"}, c.Args().Slice()...)...)
				cmd.Stdin = os.Stdin
				cmd.Stdout = c.App.Writer
				cmd.Stderr = c.App.ErrWriter
				return cmd.Run()
			}
			return fmt.Errorf(
				"\"stella upgrade\" has moved to the stellad binary.\n\n" +
					"Run: stellad upgrade\n\n" +
					"If stellad is not installed:\n" +
					"  brew upgrade stella    (Homebrew)\n" +
					"  or download both binaries from the latest GitHub release",
			)
		},
	}
}

func findCompanionDaemon() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return ""
	}
	name := "stellad"
	if runtime.GOOS == "windows" {
		name = "stellad.exe"
	}
	candidate := filepath.Join(filepath.Dir(self), name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}
