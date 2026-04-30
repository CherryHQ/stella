package main

import (
	"encoding/json"
	"fmt"

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
			&ucli.BoolFlag{
				Name:  "json",
				Usage: "Output as JSON (default)",
				Value: true,
			},
		},
		Action: func(c *ucli.Context) error {
			store, userID, dbConn, err := openRecally(c.Context)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := dbConn.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			digest, err := store.GetDigest(c.Context, userID)
			if err != nil {
				return fmt.Errorf("generate digest: %w", err)
			}

			out, err := json.MarshalIndent(digest, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal digest: %w", err)
			}
			fmt.Println(string(out))
			return nil
		},
	}
}
