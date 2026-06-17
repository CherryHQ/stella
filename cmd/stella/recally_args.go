package main

import (
	"fmt"
	"strconv"
	"strings"

	ucli "github.com/urfave/cli/v2"
)

func rejectFlagsAfterArgs(c *ucli.Context, usage string) error {
	var misplaced []string
	for _, arg := range c.Args().Slice() {
		if strings.HasPrefix(arg, "-") {
			misplaced = append(misplaced, strconv.Quote(arg))
		}
	}
	if len(misplaced) == 0 {
		return nil
	}
	// Show the misplaced option(s) and the correct order so the caller can
	// retry without guessing — the usage string already lists options first.
	return fmt.Errorf("options must be placed before positional arguments; found %s after them; usage: %s",
		strings.Join(misplaced, " "), usage)
}
