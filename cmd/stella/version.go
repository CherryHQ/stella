package main

import (
	"fmt"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/version"
)

func versionCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "version",
		Usage:    "Show the current stella version",
		Category: "System",
		Action: func(c *ucli.Context) error {
			fmt.Println(version.DisplayVersion())
			return nil
		},
	}
}
