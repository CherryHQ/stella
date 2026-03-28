package main

import (
	"fmt"
	"log/slog"
	"os"

	ucli "github.com/urfave/cli/v2"
	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/pluginhost"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	app := &ucli.App{
		Name:  "anna-plugin",
		Usage: "Internal Anna plugin helper",
		Commands: []*ucli.Command{
			toolCommand(),
			channelCommand(),
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func channelCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "channel",
		Usage: "Run a built-in channel plugin",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "work-dir"},
			&ucli.StringFlag{Name: "user-data-dir"},
		},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: anna-plugin channel <name>")
			}

			workDir := c.String("work-dir")
			if workDir == "" {
				workDir = os.Getenv("ANNA_PLUGIN_WORKDIR")
			}
			userDataDir := c.String("user-data-dir")
			if userDataDir == "" {
				userDataDir = os.Getenv("ANNA_PLUGIN_USER_DATA_DIR")
			}

			def, runtime, err := buildChannelPlugin(name, workDir, userDataDir)
			if err != nil {
				return err
			}

			return pluginhost.ServeChannel(c.Context, def, runtime, os.Stdin, os.Stdout)
		},
	}
}

func toolCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "tool",
		Usage: "Run a built-in tool plugin",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "work-dir"},
			&ucli.StringFlag{Name: "user-data-dir"},
		},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: anna-plugin tool <name>")
			}

			workDir := c.String("work-dir")
			if workDir == "" {
				workDir = os.Getenv("ANNA_PLUGIN_WORKDIR")
			}
			userDataDir := c.String("user-data-dir")
			if userDataDir == "" {
				userDataDir = os.Getenv("ANNA_PLUGIN_USER_DATA_DIR")
			}

			def, runtime, err := agenttool.BuiltinToolPlugin(name, workDir, userDataDir)
			if err != nil {
				return err
			}

			return pluginhost.ServeTool(c.Context, def, runtime, os.Stdin, os.Stdout)
		},
	}
}
