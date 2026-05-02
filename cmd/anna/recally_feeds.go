package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mmcdole/gofeed"
	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/recally"
)

func recallyFeedCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "feed",
		Usage: "Manage RSS feeds",
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
		Usage:     "Subscribe to an RSS feed",
		ArgsUsage: "<feed-url>",
		Action: func(c *ucli.Context) error {
			feedURL := c.Args().First()
			if feedURL == "" {
				return fmt.Errorf("usage: anna recally feed add <feed-url>")
			}

			store, userID, dbConn, err := openRecally(c.Context)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := dbConn.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			fp := gofeed.NewParser()
			ctx, cancel := context.WithTimeout(c.Context, 30*time.Second)
			defer cancel()

			feed, err := fp.ParseURLWithContext(feedURL, ctx)
			if err != nil {
				return fmt.Errorf("fetch feed: %w", err)
			}

			if existing, _ := store.GetFeedByURL(c.Context, userID, feedURL); existing != nil {
				return fmt.Errorf("feed already subscribed: %s", existing.ID)
			}

			newFeed, err := store.CreateFeed(c.Context, userID, feedURL, feed.Title, feed.Description, nil)
			if err != nil {
				return fmt.Errorf("create feed: %w", err)
			}

			fmt.Printf("Subscribed to feed: %s\n", newFeed.ID)
			fmt.Printf("  Title: %s\n", newFeed.Title)
			fmt.Printf("  URL: %s\n", feedURL)
			fmt.Printf("  Entries: %d available\n", len(feed.Items))
			return nil
		},
	}
}

func recallyFeedListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List subscribed RSS feeds",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{
				Name:  "json",
				Usage: "Output as JSON",
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

			feeds, err := store.ListFeeds(c.Context, userID)
			if err != nil {
				return fmt.Errorf("list feeds: %w", err)
			}

			if c.Bool("json") {
				out, err := json.MarshalIndent(feeds, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal feeds: %w", err)
				}
				fmt.Println(string(out))
				return nil
			}

			if len(feeds) == 0 {
				fmt.Println("No RSS feeds subscribed.")
				return nil
			}

			fmt.Printf("Subscribed to %d feed(s):\n\n", len(feeds))
			for _, f := range feeds {
				enabledMark := "✓"
				if !f.Enabled {
					enabledMark = "✗"
				}
				fmt.Printf("[%s] %s %s %s\n", f.ID[:8], enabledMark, f.Title, f.URL)
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
				return fmt.Errorf("usage: anna recally feed remove <feed-id>")
			}

			store, userID, dbConn, err := openRecally(c.Context)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := dbConn.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			feed, err := store.GetFeed(c.Context, feedID)
			if err != nil {
				return fmt.Errorf("get feed: %w", err)
			}
			if feed.UserID != userID {
				return fmt.Errorf("feed %s does not belong to this user", feedID[:8])
			}

			if err := store.DeleteFeed(c.Context, feedID); err != nil {
				return fmt.Errorf("remove feed: %w", err)
			}

			fmt.Printf("Feed %s removed.\n", feedID[:8])
			return nil
		},
	}
}

func recallyFeedPollCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "poll",
		Usage:     "Poll feed(s) for new entries",
		ArgsUsage: "[feed-id]",
		Flags: []ucli.Flag{
			&ucli.IntFlag{
				Name:  "limit",
				Usage: "Maximum entries to return per feed",
				Value: 20,
			},
			&ucli.BoolFlag{
				Name:  "json",
				Usage: "Output as JSON",
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

			feedID := c.Args().First()
			var feeds []recally.Feed

			if feedID != "" {
				feed, err := store.GetFeed(c.Context, feedID)
				if err != nil {
					return err
				}
				if feed.UserID != userID {
					return fmt.Errorf("feed not found or access denied")
				}
				feeds = []recally.Feed{*feed}
			} else {
				feeds, err = store.ListFeeds(c.Context, userID)
				if err != nil {
					return fmt.Errorf("list feeds: %w", err)
				}
			}

			fp := gofeed.NewParser()
			results := make([]map[string]any, 0, len(feeds))

			for _, feed := range feeds {
				if !feed.Enabled {
					continue
				}

				ctx, cancel := context.WithTimeout(c.Context, 30*time.Second)
				fetched, fetchErr := fp.ParseURLWithContext(feed.URL, ctx)
				cancel()

				if fetchErr != nil {
					results = append(results, map[string]any{
						"feed_id": feed.ID,
						"error":   fetchErr.Error(),
					})
					continue
				}

				newEntries := make([]recally.FeedEntry, 0)
				for _, item := range fetched.Items {
					entryURL := item.Link
					if entryURL == "" && item.GUID != "" {
						entryURL = item.GUID
					}
					guid := item.GUID
					if guid == "" {
						guid = entryURL
					}

					entry, err := store.CreateFeedEntry(c.Context, feed.ID, guid, entryURL, item.Title)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to create feed entry %s: %v\n", guid, err)
						continue
					}
					if entry == nil {
						continue
					}
					newEntries = append(newEntries, *entry)
				}

				now := time.Now().UTC()
				if _, err := store.UpdateFeed(c.Context, feed.ID, map[string]any{
					"last_checked_at": &now,
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update feed check time: %v\n", err)
				}

				pending, _ := store.ListPendingFeedEntries(c.Context, feed.ID, c.Int("limit"))

				results = append(results, map[string]any{
					"feed_id":     feed.ID,
					"feed_title":  feed.Title,
					"new_entries": len(newEntries),
					"pending":     pending,
				})
			}

			if c.Bool("json") {
				out, err := json.MarshalIndent(results, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal results: %w", err)
				}
				fmt.Println(string(out))
				return nil
			}

			for _, r := range results {
				if errStr, ok := r["error"].(string); ok {
					fmt.Printf("[%s] Error: %s\n", r["feed_id"].(string)[:8], errStr)
					continue
				}
				fmt.Printf("[%s] %s: %d new, %d pending\n",
					r["feed_id"].(string)[:8],
					r["feed_title"].(string),
					r["new_entries"].(int),
					len(r["pending"].([]recally.FeedEntry)))
			}
			return nil
		},
	}
}

func recallyFeedMarkCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "mark",
		Usage:     "Mark a feed entry as saved, skipped, or error",
		ArgsUsage: "<entry-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:     "status",
				Usage:    "New status: saved, skipped, error",
				Required: true,
			},
			&ucli.StringFlag{
				Name:  "article-id",
				Usage: "Article ID (required when status=saved)",
			},
			&ucli.StringFlag{
				Name:  "error",
				Usage: "Error message (when status=error)",
			},
		},
		Action: func(c *ucli.Context) error {
			entryID := c.Args().First()
			if entryID == "" {
				return fmt.Errorf("usage: anna recally feed mark <entry-id> --status <status>")
			}

			status := recally.RSSEntryStatus(c.String("status"))
			switch status {
			case recally.EntryStatusSaved, recally.EntryStatusSkipped, recally.EntryStatusError:
				// Valid
			default:
				return fmt.Errorf("invalid status: %s (must be saved, skipped, or error)", status)
			}

			if status == recally.EntryStatusSaved && c.String("article-id") == "" {
				return fmt.Errorf("--article-id required when marking as saved")
			}

			store, userID, dbConn, err := openRecally(c.Context)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := dbConn.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			feedEntry, err := store.GetFeedEntry(c.Context, entryID)
			if err != nil {
				return fmt.Errorf("get entry: %w", err)
			}
			feed, err := store.GetFeed(c.Context, feedEntry.FeedID)
			if err != nil {
				return fmt.Errorf("get feed: %w", err)
			}
			if feed.UserID != userID {
				return fmt.Errorf("entry %s does not belong to this user", entryID[:8])
			}

			var articleID *string
			if aid := c.String("article-id"); aid != "" {
				articleID = &aid
			}

			entry, err := store.MarkFeedEntry(c.Context, entryID, status, articleID, c.String("error"))
			if err != nil {
				return fmt.Errorf("mark entry: %w", err)
			}

			fmt.Printf("Entry %s marked as %s (attempts: %d)\n",
				entryID[:8], status, entry.Attempts)
			return nil
		},
	}
}
