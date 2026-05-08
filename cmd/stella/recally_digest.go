package main

import (
	apiclient "github.com/CherryHQ/stella/api/client"
	ucli "github.com/urfave/cli/v2"
)

func recallyDigestCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "digest",
		Usage: "Generate a daily reading digest",
		Description: `Outputs a structured JSON summary of reading activity:
- Articles saved yesterday
- Unread/read/archived/starred counts
- Articles worth revisiting (unread > 3 days old)
- Top tags from this week`,
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON (default)", Value: true},
		},
		Action: func(c *ucli.Context) error {
			api, err := recallyAPI()
			if err != nil {
				return err
			}
			resp, err := api.GetDigest(c.Context)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			var digest apiclient.Digest
			if err := decodeJSON(resp, &digest); err != nil {
				return err
			}
			return printJSON(digest)
		},
	}
}
