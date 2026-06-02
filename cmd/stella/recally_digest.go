package main

import (
	"net/http"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
)

func recallyDigestCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "digest",
		Usage: "Generate a daily reading digest",
		Description: `Summarizes reading activity:
- Articles saved yesterday
- Unread/read/archived/starred counts
- Articles worth revisiting (unread > 3 days old)
- Top tags from this week

Running "digest" with no subcommand generates today's digest. Use
"digest save" to persist a snapshot with a narrative.`,
		Flags: []ucli.Flag{jsonFlag()},
		Action: func(c *ucli.Context) error {
			digest, err := apiclient.Call[apiclient.Digest](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetDigest(c.Context)
			})
			if err != nil {
				return err
			}
			if isJSON(c) {
				return printJSON(c, digest)
			}
			o := stdout(c)
			o.printf("digest:    %s\n", digest.Date.Format("2006-01-02"))
			o.printf("total:     %d\n", digest.TotalArticles)
			o.printf("unread:    %d\n", digest.UnreadCount)
			o.printf("read:      %d\n", digest.ReadCount)
			o.printf("archived:  %d\n", digest.ArchivedCount)
			o.printf("starred:   %d\n", digest.StarredCount)
			o.printf("yesterday: %d\n", digest.SavedYesterdayCount)
			o.printf("revisit:   %d\n", digest.WorthRevisitingCount)
			return o.Err()
		},
		Subcommands: []*ucli.Command{
			recallyDigestSaveCommand(),
		},
	}
}

func recallyDigestSaveCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "save",
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
			jsonFlag(),
		},
		Action: func(c *ucli.Context) error {
			narrative := c.String("narrative")
			body := apiclient.SaveDigestJSONRequestBody{Narrative: narrative}
			if d := c.String("date"); d != "" {
				body.Date = &d
			}
			stored, err := apiclient.Call[apitypes.StoredDigest](func(api *apiclient.Client) (*http.Response, error) {
				return api.SaveDigest(c.Context, body)
			})
			if err != nil {
				return err
			}
			if isJSON(c) {
				return printJSON(c, stored)
			}
			o := stdout(c)
			o.printf("saved digest %s (%s)\n", shortID(stored.Id), stored.Date)
			return o.Err()
		},
	}
}
