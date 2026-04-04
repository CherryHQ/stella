package main

import (
	"context"
	"fmt"

	ucli "github.com/urfave/cli/v2"
)

func pluginCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "plugin",
		Usage: "Manage plugins",
		Subcommands: []*ucli.Command{
			pluginListCommand(),
		},
		Action: func(c *ucli.Context) error {
			return pluginListAction(c)
		},
	}
}

func pluginListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List all configured plugins",
		Action: pluginListAction,
	}
}

func pluginListAction(c *ucli.Context) error {
	store, err := openStore()
	if err != nil {
		return err
	}

	plugins, err := store.ListPlugins(context.Background())
	if err != nil {
		return err
	}

	if len(plugins) == 0 {
		fmt.Println("No plugins configured.")
		return nil
	}

	fmt.Printf("%-24s %-10s %-8s %s\n", "ID", "KIND", "ENABLED", "VERSION")
	for _, p := range plugins {
		fmt.Printf("%-24s %-10s %-8s %s\n", p.ID, p.Kind, yesNo(p.Enabled), p.Version)
	}
	return nil
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

