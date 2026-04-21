package main

import (
	"fmt"
	"os"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/builddeps"
)

func main() {
	app := newApp(builddeps.Syncer{
		SyncSkills: builddeps.SyncSystemSkills,
		SyncTools:  builddeps.SyncEmbeddedTools,
	})
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newApp(syncer builddeps.Syncer) *ucli.App {
	return &ucli.App{
		Name:  "builddeps",
		Usage: "Prepare third-party tools and system skills before building anna",
		Flags: syncFlags(),
		Commands: []*ucli.Command{
			syncCommand(syncer),
		},
		Action: func(c *ucli.Context) error {
			return syncAction(c, syncer)
		},
	}
}

func syncCommand(syncer builddeps.Syncer) *ucli.Command {
	return &ucli.Command{
		Name:   "sync",
		Usage:  "Sync pre-build third-party tools and system skills",
		Flags:  syncFlags(),
		Action: func(c *ucli.Context) error { return syncAction(c, syncer) },
	}
}

func syncFlags() []ucli.Flag {
	return []ucli.Flag{
		&ucli.BoolFlag{Name: "skills", Usage: "sync embedded system skills"},
		&ucli.BoolFlag{Name: "tools", Usage: "sync embedded third-party binaries"},
		&ucli.StringFlag{Name: "goos", Usage: "target GOOS for embedded binaries"},
		&ucli.StringFlag{Name: "goarch", Usage: "target GOARCH for embedded binaries"},
		&ucli.StringFlag{Name: "lark-ref", Usage: "git ref to check out from larksuite/cli"},
		&ucli.StringFlag{Name: "workdir", Usage: "repo root containing internal/resources", Value: "."},
		&ucli.BoolFlag{Name: "isolated", Usage: "ignored compatibility flag", Hidden: true},
	}
}

func syncAction(c *ucli.Context, syncer builddeps.Syncer) error {
	cfg := builddeps.Config{
		WorkDir:    c.String("workdir"),
		SyncSkills: c.Bool("skills"),
		SyncTools:  c.Bool("tools"),
		GOOS:       c.String("goos"),
		GOARCH:     c.String("goarch"),
		LarkRef:    c.String("lark-ref"),
	}
	return syncer.Run(c.Context, cfg)
}
