package main

import ucli "github.com/urfave/cli/v2"

func recallyCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "recally",
		Usage: "Reading assistant - save, organize, and recall web content",
		Subcommands: []*ucli.Command{
			recallySaveCommand(),
			recallyListCommand(),
			recallySearchCommand(),
			recallyReadCommand(),
			recallyUpdateCommand(),
			recallyDeleteCommand(),
			recallyFeedCommand(),
			recallyDigestCommand(),
			recallyDigestSaveCommand(),
		},
	}
}
