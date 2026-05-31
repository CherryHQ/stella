package main

import (
	"fmt"
	"net/http"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
)

func recallyFeedCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "feed",
		Usage: "Manage RSS feeds",
		Description: `Subscribe to RSS feeds and poll them for new entries. New entries can
be saved as articles in your library or skipped. Use "feed poll" to
check all feeds at once or target a single feed by ID.`,
		Subcommands: []*ucli.Command{
			recallyFeedAddCommand(),
			recallyFeedListCommand(),
			recallyFeedRemoveCommand(),
			recallyFeedPollCommand(),
			recallyFeedMarkCommand(),
		},
	}
}

func recallyFeedAddCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "add",
		Usage:     "Subscribe to an RSS feed (server fetches feed metadata)",
		ArgsUsage: "<feed-url>",
		Action: func(c *ucli.Context) error {
			feedURL := c.Args().First()
			if feedURL == "" {
				return fmt.Errorf("usage: stella recally feed add <feed-url>")
			}
			feed, err := apiclient.Call[apiclient.Feed](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateFeed(c.Context, apiclient.CreateFeedJSONRequestBody{Url: feedURL})
			})
			if err != nil {
				return err
			}
			fmt.Printf("Subscribed to feed: %s\n", feed.Id)
			fmt.Printf("  Title: %s\n", feed.Title)
			fmt.Printf("  URL: %s\n", feed.Url)
			return nil
		},
	}
}

func recallyFeedListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List subscribed RSS feeds",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			list, err := apiclient.Call[apiclient.FeedList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListFeeds(c.Context, &apiclient.ListFeedsParams{})
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Feeds)
			}
			if len(list.Feeds) == 0 {
				fmt.Println("No RSS feeds subscribed.")
				return nil
			}
			fmt.Printf("Subscribed to %d feed(s):\n\n", len(list.Feeds))
			for _, f := range list.Feeds {
				enabledMark := "✓"
				if !f.Enabled {
					enabledMark = "✗"
				}
				fmt.Printf("[%s] %s %s %s\n", shortID(f.Id), enabledMark, f.Title, f.Url)
				if f.LastCheckedAt != nil {
					fmt.Printf("    Last checked: %s\n", f.LastCheckedAt.Format("2006-01-02 15:04"))
				}
				fmt.Printf("    Check interval: %s\n", f.CheckInterval)
				fmt.Println()
			}
			return nil
		},
	}
}

func recallyFeedRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Aliases:   []string{"rm"},
		Usage:     "Unsubscribe from an RSS feed",
		ArgsUsage: "<feed-id>",
		Action: func(c *ucli.Context) error {
			feedID := c.Args().First()
			if feedID == "" {
				return fmt.Errorf("usage: stella recally feed remove <feed-id>")
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteFeed(c.Context, feedID)
			}); err != nil {
				return err
			}
			fmt.Printf("Feed %s removed.\n", shortID(feedID))
			return nil
		},
	}
}

func recallyFeedPollCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "poll",
		Usage:     "Poll feed(s) for new entries (server-side fetch)",
		ArgsUsage: "[feed-id]",
		Flags: []ucli.Flag{
			&ucli.IntFlag{Name: "limit", Usage: "Maximum new entries to return per feed", Value: 20},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			api, err := apiclient.NewAPIClient()
			if err != nil {
				return err
			}
			feedIDs := []string{}
			if id := c.Args().First(); id != "" {
				feedIDs = append(feedIDs, id)
			} else {
				resp, err := api.ListFeeds(c.Context, &apiclient.ListFeedsParams{})
				if err != nil {
					return apiclient.WrapServerErr(err)
				}
				defer resp.Body.Close() //nolint:errcheck
				var list apiclient.FeedList
				if err := apiclient.DecodeJSON(resp, &list); err != nil {
					return err
				}
				for _, f := range list.Feeds {
					if f.Enabled {
						feedIDs = append(feedIDs, f.Id)
					}
				}
			}

			results := make([]apiclient.FeedPollResult, 0, len(feedIDs))
			for _, id := range feedIDs {
				resp, err := api.PollFeed(c.Context, id, &apiclient.PollFeedParams{Limit: apiclient.Ptr(c.Int("limit"))})
				if err != nil {
					return apiclient.WrapServerErr(err)
				}
				var pr apiclient.FeedPollResult
				decodeErr := apiclient.DecodeJSON(resp, &pr)
				_ = resp.Body.Close()
				if decodeErr != nil {
					return decodeErr
				}
				results = append(results, pr)
			}

			if c.Bool("json") {
				return printJSON(results)
			}
			for _, r := range results {
				if r.Error != nil && *r.Error != "" {
					fmt.Printf("[%s] Error: %s\n", shortID(r.Feed.Id), *r.Error)
					continue
				}
				fmt.Printf("[%s] %s: %d new\n", shortID(r.Feed.Id), r.Feed.Title, len(r.NewEntries))
			}
			return nil
		},
	}
}

func recallyFeedMarkCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "mark",
		Usage:     "Mark a feed entry as saved, skipped, or error",
		ArgsUsage: "<feed-id> <entry-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "status", Usage: "New status: saved, skipped, error", Required: true},
			&ucli.StringFlag{Name: "article-id", Usage: "Article ID (required when status=saved)"},
			&ucli.StringFlag{Name: "error", Usage: "Error message (when status=error)"},
		},
		Action: func(c *ucli.Context) error {
			args := c.Args().Slice()
			if len(args) < 2 {
				return fmt.Errorf("usage: stella recally feed mark <feed-id> <entry-id> --status <status>")
			}
			feedID, entryID := args[0], args[1]

			status := apiclient.FeedEntryStatus(c.String("status"))
			switch status {
			case apitypes.FeedEntryStatusSaved, apitypes.FeedEntryStatusSkipped, apitypes.FeedEntryStatusError:
			default:
				return fmt.Errorf("invalid status: %s (must be saved, skipped, or error)", status)
			}
			if status == apitypes.FeedEntryStatusSaved && c.String("article-id") == "" {
				return fmt.Errorf("--article-id required when marking as saved")
			}
			body := apiclient.UpdateFeedEntryJSONRequestBody{Status: status}
			if v := c.String("article-id"); v != "" {
				body.ArticleId = &v
			}
			if v := c.String("error"); v != "" {
				body.ErrorMsg = &v
			}
			entry, err := apiclient.Call[apiclient.FeedEntry](func(api *apiclient.Client) (*http.Response, error) {
				return api.UpdateFeedEntry(c.Context, feedID, entryID, body)
			})
			if err != nil {
				return err
			}
			fmt.Printf("Entry %s marked as %s (attempts: %d)\n", shortID(entryID), status, entry.Attempts)
			return nil
		},
	}
}
