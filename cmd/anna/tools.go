package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/tools"
)

func toolsCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "tools",
		Usage: "Manage downloadable CLI tools",
		Subcommands: []*ucli.Command{
			toolsListCommand(),
			toolsInstallCommand(),
			toolsUpgradeCommand(),
		},
		Action: func(c *ucli.Context) error {
			return toolsListAction()
		},
	}
}

func toolsListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "Show all downloadable tools and their status",
		Action: func(c *ucli.Context) error {
			return toolsListAction()
		},
	}
}

func toolsListAction() error {
	binDir := tools.BinDir(config.AnnaHome())
	statuses := tools.Status(binDir, tools.Platform())

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tVERSION\tSTATUS")
	for _, s := range statuses {
		status := "not installed"
		if s.Installed && s.Current {
			status = "installed"
		} else if s.Installed {
			status = "outdated"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.Version, status)
	}
	return w.Flush()
}

func toolsInstallCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "install",
		Usage:     "Download and install tools (all if no args)",
		ArgsUsage: "[name...]",
		Action: func(c *ucli.Context) error {
			binDir := tools.BinDir(config.AnnaHome())
			platform := tools.Platform()

			if c.NArg() == 0 {
				if err := tools.DownloadAll(c.Context, binDir, platform); err != nil {
					return err
				}
				fmt.Println("All tools installed.")
				return nil
			}

			for _, name := range c.Args().Slice() {
				tool := tools.FindTool(name)
				if tool == nil {
					return fmt.Errorf("unknown tool: %s", name)
				}
				if err := tools.Download(c.Context, tool, binDir, platform); err != nil {
					return err
				}
				fmt.Printf("Installed %s %s\n", tool.Name, tool.Version)
			}
			return nil
		},
	}
}

func toolsUpgradeCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "upgrade",
		Usage:     "Upgrade tools to latest configured versions (all if no args)",
		ArgsUsage: "[name...]",
		Action: func(c *ucli.Context) error {
			binDir := tools.BinDir(config.AnnaHome())
			platform := tools.Platform()

			if c.NArg() == 0 {
				if err := tools.DownloadAll(c.Context, binDir, platform); err != nil {
					return err
				}
				fmt.Println("All tools upgraded.")
				return nil
			}

			for _, name := range c.Args().Slice() {
				tool := tools.FindTool(name)
				if tool == nil {
					return fmt.Errorf("unknown tool: %s", name)
				}
				if err := tools.Download(c.Context, tool, binDir, platform); err != nil {
					return err
				}
				fmt.Printf("Upgraded %s to %s\n", tool.Name, tool.Version)
			}
			return nil
		},
	}
}
