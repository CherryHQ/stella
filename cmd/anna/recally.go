package main

import (
	ucli "github.com/urfave/cli/v2"
	apiclient "github.com/vaayne/anna/api/client"
)

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
		},
	}
}

// recallyAPI returns an API client authenticated via ANNA_TOKEN. The CLI is
// purely an HTTP client; the running anna server owns the database and the
// markdown library on disk.
func recallyAPI() (*apiclient.Client, error) {
	return newAPIClient()
}
