package main

import (
	"fmt"
	"strings"

	ucli "github.com/urfave/cli/v2"
)

func rejectFlagsAfterArgs(c *ucli.Context, usage string) error {
	for _, arg := range c.Args().Slice() {
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("flags must be placed before positional arguments; usage: %s", usage)
		}
	}
	return nil
}
