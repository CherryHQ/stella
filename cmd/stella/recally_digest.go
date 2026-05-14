package main

import (
	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
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

func recallyDigestSaveCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "digest-save",
		Usage: "Persist a digest snapshot with an AI-generated narrative",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:     "narrative",
				Aliases:  []string{"n"},
				Usage:    "The narrative text to store",
				Required: true,
			},
			&ucli.StringFlag{
				Name:  "date",
				Usage: "Digest date (YYYY-MM-DD); defaults to today",
			},
		},
		Action: func(c *ucli.Context) error {
			api, err := recallyAPI()
			if err != nil {
				return err
			}
			narrative := c.String("narrative")
			body := apiclient.SaveDigestJSONRequestBody{Narrative: narrative}
			if d := c.String("date"); d != "" {
				body.Date = &d
			}
			resp, err := api.SaveDigest(c.Context, body)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			var stored apitypes.StoredDigest
			if err := decodeJSON(resp, &stored); err != nil {
				return err
			}
			return printJSON(stored)
		},
	}
}
