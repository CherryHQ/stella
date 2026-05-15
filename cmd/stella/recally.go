package main

import ucli "github.com/urfave/cli/v2"

func recallyCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "recally",
		Usage:    "Reading assistant - save, organize, and recall web content",
		Category: "Content",
		Description: `Save articles, subscribe to RSS feeds, and generate reading digests.
Content is stored in a Markdown library managed by the stella server;
these commands let you browse, search, and curate it from the terminal.`,
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
