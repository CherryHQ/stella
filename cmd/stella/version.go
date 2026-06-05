package main

import (
	"fmt"
	"strings"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/version"
)

func versionCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "version",
		Usage:    "Show the current stella version",
		Category: "System",
		Action: func(c *ucli.Context) error {
			fmt.Println(displayVersion())
			return nil
		},
	}
}

func displayVersion() string {
	normalized := normalizeVersion(version.Version)
	if normalized == "" {
		return "dev"
	}
	return normalized
}

func normalizeVersion(v string) string {
	trimmed := strings.TrimSpace(v)
	trimmed = strings.TrimPrefix(trimmed, "v")
	if trimmed == "" {
		return ""
	}
	for part := range strings.SplitSeq(trimmed, ".") {
		if part == "" {
			return ""
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return trimmed
			}
		}
	}
	return trimmed
}
