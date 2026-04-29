package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/recally"
)

// recallyCommand returns the recally CLI subcommand.
func recallyCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "recally",
		Usage: "Reading assistant - save, organize, and recall web content",
		Flags: []ucli.Flag{
			&ucli.Int64Flag{
				Name:  "user-id",
				Usage: "User ID (only used when ANNA_USER_ID env var is not set)",
			},
		},
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

// resolveUserID returns the user ID from env var (authoritative) or flag.
// When ANNA_USER_ID is set, it takes precedence and --user-id is ignored.
// Returns error if neither is available.
func resolveUserID(c *ucli.Context) (int64, error) {
	envUserID := os.Getenv("ANNA_USER_ID")
	if envUserID != "" {
		userID, err := strconv.ParseInt(envUserID, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid ANNA_USER_ID env var: %w", err)
		}
		flagUserID := c.Int64("user-id")
		if flagUserID != 0 && flagUserID != userID {
			fmt.Fprintf(os.Stderr, "Warning: ANNA_USER_ID (%d) overrides --user-id flag (%d)\n", userID, flagUserID)
		}
		return userID, nil
	}

	// No env var - use flag
	userID := c.Int64("user-id")
	if userID == 0 {
		return 0, fmt.Errorf("ANNA_USER_ID env var or --user-id flag required")
	}
	return userID, nil
}

// openRecallyStore opens the DB and returns a recally Store.
func openRecallyStore() (*recally.Store, *sql.DB, error) {
	dbPath := config.DBPath()
	db, err := db.OpenDB(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	return recally.NewStore(db), db, nil
}

// recallySaveCommand handles `anna recally save`.
func recallySaveCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "save",
		Usage: "Save an article to your library",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:     "url",
				Usage:    "Article URL (required)",
				Required: true,
			},
			&ucli.StringFlag{
				Name:  "canonical-url",
				Usage: "Canonical URL (optional, overrides computed canonical URL for deduplication)",
			},
			&ucli.StringFlag{
				Name:  "title",
				Usage: "Article title",
				Value: "",
			},
			&ucli.StringFlag{
				Name:  "summary",
				Usage: "Article summary",
				Value: "",
			},
			&ucli.StringSliceFlag{
				Name:  "tags",
				Usage: "Article tags (can be used multiple times)",
			},
			&ucli.StringFlag{
				Name:  "source-type",
				Usage: "Source type: web, twitter, youtube, github, rss, pdf",
				Value: "web",
			},
			&ucli.StringFlag{
				Name:  "author",
				Usage: "Article author",
				Value: "",
			},
			&ucli.StringFlag{
				Name:  "content-file",
				Usage: "Path to file containing article content (stdin used if not provided)",
			},
			&ucli.StringFlag{
				Name:  "metadata",
				Usage: "JSON metadata string",
				Value: "{}",
			},
			&ucli.StringFlag{
				Name:  "published-at",
				Usage: "Original publication date (RFC3339)",
			},
		},
		Action: func(c *ucli.Context) error {
			userID, err := resolveUserID(c)
			if err != nil {
				return err
			}

			// Read content from file or stdin
			var content string
			contentFile := c.String("content-file")
			if contentFile != "" {
				data, err := os.ReadFile(contentFile)
				if err != nil {
					return fmt.Errorf("read content file: %w", err)
				}
				content = string(data)
			} else {
				// Try to read from stdin
				stat, err := os.Stdin.Stat()
				if err == nil && stat.Size() > 0 {
					data, err := io.ReadAll(os.Stdin)
					if err != nil {
						return fmt.Errorf("read stdin: %w", err)
					}
					content = string(data)
				}
			}

			// Parse metadata
			metadata := make(map[string]string)
			if metaStr := c.String("metadata"); metaStr != "" && metaStr != "{}" {
				if err := json.Unmarshal([]byte(metaStr), &metadata); err != nil {
					return fmt.Errorf("parse metadata JSON: %w", err)
				}
			}

			// Parse published-at
			var publishedAt *time.Time
			if pubStr := c.String("published-at"); pubStr != "" {
				t, err := time.Parse(time.RFC3339, pubStr)
				if err != nil {
					return fmt.Errorf("parse published-at: %w", err)
				}
				publishedAt = &t
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			req := recally.SaveRequest{
				URL:          c.String("url"),
				CanonicalURL: c.String("canonical-url"),
				SourceType:   recally.SourceType(c.String("source-type")),
				Title:        c.String("title"),
				Author:       c.String("author"),
				Summary:      c.String("summary"),
				Tags:         c.StringSlice("tags"),
				Content:      content,
				Metadata:     metadata,
				PublishedAt:  publishedAt,
			}

			article, isNew, err := store.SaveArticle(c.Context, userID, req)
			if err != nil {
				return fmt.Errorf("save article: %w", err)
			}

			// Write content to file
			annaHome := config.AnnaHome()
			fm := recally.NewFileManager(annaHome)
			filePath := fm.ArticlePath(userID, article.Title, article.SavedAt)
			if err := fm.WriteArticle(filePath, article, content); err != nil {
				return fmt.Errorf("write article file: %w", err)
			}

			// Update article with file path
			relPath := fm.RelativePath(filePath)
			if err := store.UpdateArticleFilePath(c.Context, article.ID, relPath); err != nil {
				return fmt.Errorf("update file path: %w", err)
			}
			article.FilePath = relPath

			// Output result
			result := map[string]any{
				"id":        article.ID,
				"file_path": article.FilePath,
				"created":   isNew,
			}
			if isNew {
				result["message"] = "Article saved successfully"
			} else {
				result["message"] = "Article already exists, updated metadata"
			}
			out, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}
}

// recallyListCommand handles `anna recally list`.
func recallyListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List saved articles",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:  "status",
				Usage: "Filter by status: unread, read, archived",
			},
			&ucli.StringFlag{
				Name:  "source-type",
				Usage: "Filter by source type: web, twitter, youtube, github, rss, pdf",
			},
			&ucli.BoolFlag{
				Name:  "starred",
				Usage: "Show only starred articles",
			},
			&ucli.IntFlag{
				Name:  "limit",
				Usage: "Maximum number of articles to return",
				Value: 50,
			},
			&ucli.BoolFlag{
				Name:  "json",
				Usage: "Output as JSON",
			},
		},
		Action: func(c *ucli.Context) error {
			userID, err := resolveUserID(c)
			if err != nil {
				return err
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			starred := c.Bool("starred")
			filter := recally.ArticleFilter{
				Status:     recally.ArticleStatus(c.String("status")),
				SourceType: recally.SourceType(c.String("source-type")),
				Starred:    &starred,
				Limit:      c.Int("limit"),
			}

			articles, err := store.ListArticles(c.Context, userID, filter)
			if err != nil {
				return fmt.Errorf("list articles: %w", err)
			}

			if c.Bool("json") {
				out, _ := json.MarshalIndent(articles, "", "  ")
				fmt.Println(string(out))
				return nil
			}

			if len(articles) == 0 {
				fmt.Println("No articles found.")
				return nil
			}

			fmt.Printf("Found %d article(s):\n\n", len(articles))
			for _, a := range articles {
				starMark := " "
				if a.Starred {
					starMark = "★"
				}
				fmt.Printf("[%s] %s %s\n", a.ID[:8], starMark, a.Title)
				fmt.Printf("    URL: %s\n", a.URL)
				if a.Summary != "" {
					summary := a.Summary
					if len(summary) > 100 {
						summary = summary[:97] + "..."
					}
					fmt.Printf("    %s\n", summary)
				}
				fmt.Printf("    Status: %s | Source: %s | Saved: %s\n",
					a.Status, a.SourceType, a.SavedAt.Format("2006-01-02"))
				fmt.Println()
			}
			return nil
		},
	}
}

// recallySearchCommand handles `anna recally search <query>`.
// Phase 1 MVP: Uses LIKE-based search (FTS5 deferred to future phase).
func recallySearchCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "search",
		Usage:     "Search articles by title, summary, tags, or author (metadata-based search)",
		ArgsUsage: "<query>",
		Flags: []ucli.Flag{
			&ucli.IntFlag{
				Name:  "limit",
				Usage: "Maximum number of results",
				Value: 50,
			},
			&ucli.BoolFlag{
				Name:  "json",
				Usage: "Output as JSON",
			},
		},
		Action: func(c *ucli.Context) error {
			query := c.Args().First()
			if query == "" {
				return fmt.Errorf("usage: anna recally search <query>")
			}

			userID, err := resolveUserID(c)
			if err != nil {
				return err
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			// Phase 1: LIKE-based search. Future phases will use FTS5.
			articles, err := store.SearchArticles(c.Context, userID, query, c.Int("limit"))
			if err != nil {
				return fmt.Errorf("search articles: %w", err)
			}

			if c.Bool("json") {
				out, _ := json.MarshalIndent(articles, "", "  ")
				fmt.Println(string(out))
				return nil
			}

			if len(articles) == 0 {
				fmt.Println("No articles found matching your query.")
				return nil
			}

			fmt.Printf("Found %d article(s) matching %q:\n\n", len(articles), query)
			for _, a := range articles {
				starMark := " "
				if a.Starred {
					starMark = "★"
				}
				fmt.Printf("[%s] %s %s\n", a.ID[:8], starMark, a.Title)
				fmt.Printf("    URL: %s\n", a.URL)
				if a.Summary != "" {
					summary := a.Summary
					if len(summary) > 80 {
						summary = summary[:77] + "..."
					}
					fmt.Printf("    %s\n", summary)
				}
				fmt.Printf("    Status: %s | Source: %s\n", a.Status, a.SourceType)
				fmt.Println()
			}
			return nil
		},
	}
}

// recallyReadCommand handles `anna recally read <id>`.
func recallyReadCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "read",
		Usage:     "Read full article content",
		ArgsUsage: "<article-id>",
		Action: func(c *ucli.Context) error {
			articleID := c.Args().First()
			if articleID == "" {
				return fmt.Errorf("usage: anna recally read <article-id>")
			}

			userID, err := resolveUserID(c)
			if err != nil {
				return err
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			article, err := store.GetArticle(c.Context, articleID)
			if err != nil {
				return err
			}

			// Verify user ownership
			if article.UserID != userID {
				return fmt.Errorf("article not found or access denied")
			}

			// Read file content
			annaHome := config.AnnaHome()
			fm := recally.NewFileManager(annaHome)
			filePath := article.FilePath
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(annaHome, filePath)
			}

			content, err := fm.ReadArticleFull(filePath)
			if err != nil {
				// Try to just output metadata if file is missing
				fmt.Fprintf(os.Stderr, "Warning: could not read file: %v\n", err)
				content = article.Summary
			}

			fmt.Println(content)
			return nil
		},
	}
}

// recallyUpdateCommand handles `anna recally update <id>`.
func recallyUpdateCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "update",
		Usage:     "Update article metadata",
		ArgsUsage: "<article-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:  "status",
				Usage: "New status: unread, read, archived",
			},
			&ucli.BoolFlag{
				Name:  "starred",
				Usage: "Star or unstar the article",
			},
			&ucli.StringFlag{
				Name:  "summary",
				Usage: "New summary",
			},
			&ucli.StringSliceFlag{
				Name:  "tags",
				Usage: "New tags (replaces existing)",
			},
		},
		Action: func(c *ucli.Context) error {
			articleID := c.Args().First()
			if articleID == "" {
				return fmt.Errorf("usage: anna recally update <article-id>")
			}

			userID, err := resolveUserID(c)
			if err != nil {
				return err
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			// Verify ownership
			article, err := store.GetArticle(c.Context, articleID)
			if err != nil {
				return err
			}
			if article.UserID != userID {
				return fmt.Errorf("article not found or access denied")
			}

			updates := make(map[string]any)
			if c.IsSet("status") {
				updates["status"] = c.String("status")
			}
			if c.IsSet("starred") {
				updates["starred"] = c.Bool("starred")
			}
			if c.IsSet("summary") {
				updates["summary"] = c.String("summary")
			}
			if c.IsSet("tags") {
				updates["tags"] = c.StringSlice("tags")
			}

			if len(updates) == 0 {
				return fmt.Errorf("no updates specified")
			}

			updated, err := store.UpdateArticle(c.Context, articleID, updates)
			if err != nil {
				return fmt.Errorf("update article: %w", err)
			}

			// Update file frontmatter if file exists
			annaHome := config.AnnaHome()
			fm := recally.NewFileManager(annaHome)
			filePath := updated.FilePath
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(annaHome, filePath)
			}

			if content, err := fm.ReadArticleFull(filePath); err == nil {
				// Extract body, rewrite with updated article
				_, body := splitFrontmatter(content)
				if err := fm.WriteArticle(filePath, updated, strings.TrimSpace(body)); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update file frontmatter: %v\n", err)
				}
			}

			fmt.Printf("Article %s updated successfully.\n", articleID[:8])
			return nil
		},
	}
}

// recallyDeleteCommand handles `anna recally delete <id>`.
func recallyDeleteCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "delete",
		Usage:     "Delete an article from library",
		ArgsUsage: "<article-id>",
		Action: func(c *ucli.Context) error {
			articleID := c.Args().First()
			if articleID == "" {
				return fmt.Errorf("usage: anna recally delete <article-id>")
			}

			userID, err := resolveUserID(c)
			if err != nil {
				return err
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			// Verify ownership and get file path
			article, err := store.GetArticle(c.Context, articleID)
			if err != nil {
				return err
			}
			if article.UserID != userID {
				return fmt.Errorf("article not found or access denied")
			}

			// Delete from DB
			if err := store.DeleteArticle(c.Context, articleID); err != nil {
				return fmt.Errorf("delete article: %w", err)
			}

			// Delete file
			annaHome := config.AnnaHome()
			fm := recally.NewFileManager(annaHome)
			filePath := article.FilePath
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(annaHome, filePath)
			}
			if err := fm.DeleteArticle(filePath); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to delete file: %v\n", err)
			}

			fmt.Printf("Article %s deleted.\n", articleID[:8])
			return nil
		},
	}
}

// recallyFeedCommand handles `anna recally feed` subcommands.
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

// recallyFeedAddCommand handles `anna recally feed add <url>`.
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

			userID, err := resolveUserID(c)
			if err != nil {
				return err
			}

			// Fetch feed metadata
			fp := gofeed.NewParser()
			ctx, cancel := context.WithTimeout(c.Context, 30*time.Second)
			defer cancel()

			feed, err := fp.ParseURLWithContext(feedURL, ctx)
			if err != nil {
				return fmt.Errorf("fetch feed: %w", err)
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			// Check for duplicate
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

// recallyFeedListCommand handles `anna recally feed list`.
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
			userID, err := resolveUserID(c)
			if err != nil {
				return err
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			feeds, err := store.ListFeeds(c.Context, userID)
			if err != nil {
				return fmt.Errorf("list feeds: %w", err)
			}

			if c.Bool("json") {
				out, _ := json.MarshalIndent(feeds, "", "  ")
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

// recallyFeedRemoveCommand handles `anna recally feed remove <id>`.
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

			_, err := resolveUserID(c)
			if err != nil {
				return err
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			if err := store.DeleteFeed(c.Context, feedID); err != nil {
				return fmt.Errorf("remove feed: %w", err)
			}

			fmt.Printf("Feed %s removed.\n", feedID[:8])
			return nil
		},
	}
}

// recallyFeedPollCommand handles `anna recally feed poll [<id>]`.
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
			userID, err := resolveUserID(c)
			if err != nil {
				return err
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			feedID := c.Args().First()
			var feeds []recally.Feed

			if feedID != "" {
				// Poll specific feed
				feed, err := store.GetFeed(c.Context, feedID)
				if err != nil {
					return err
				}
				if feed.UserID != userID {
					return fmt.Errorf("feed not found or access denied")
				}
				feeds = []recally.Feed{*feed}
			} else {
				// Poll all feeds
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
				fetched, err := fp.ParseURLWithContext(feed.URL, ctx)
				cancel()

				if err != nil {
					results = append(results, map[string]any{
						"feed_id": feed.ID,
						"error":   err.Error(),
					})
					continue
				}

				// Create entries for new items
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
						// Entry likely already exists, skip
						continue
					}
					newEntries = append(newEntries, *entry)
				}

				// Update feed check time
				now := time.Now().UTC()
				if _, err := store.UpdateFeed(c.Context, feed.ID, map[string]any{
					"last_checked_at": &now,
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update feed check time: %v\n", err)
				}

				// Get pending entries for output
				pending, _ := store.ListPendingFeedEntries(c.Context, feed.ID, c.Int("limit"))

				results = append(results, map[string]any{
					"feed_id":     feed.ID,
					"feed_title":  feed.Title,
					"new_entries": len(newEntries),
					"pending":     pending,
				})
			}

			if c.Bool("json") {
				out, _ := json.MarshalIndent(results, "", "  ")
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

// recallyDigestCommand handles `anna recally digest`.
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
			userID, err := resolveUserID(c)
			if err != nil {
				return err
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

			digest, err := store.GetDigest(c.Context, userID)
			if err != nil {
				return fmt.Errorf("generate digest: %w", err)
			}

			out, _ := json.MarshalIndent(digest, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}
}

// recallyFeedMarkCommand handles `anna recally feed mark <entry-id>`.
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

			_, err := resolveUserID(c)
			if err != nil {
				return err
			}

			status := recally.RSSEntryStatus(c.String("status"))
			switch status {
			case recally.EntryStatusSaved, recally.EntryStatusSkipped, recally.EntryStatusError:
				// Valid
			default:
				return fmt.Errorf("invalid status: %s (must be saved, skipped, or error)", status)
			}

			// Validate required flags
			if status == recally.EntryStatusSaved && c.String("article-id") == "" {
				return fmt.Errorf("--article-id required when marking as saved")
			}

			store, dbConn, err := openRecallyStore()
			if err != nil {
				return err
			}
			defer dbConn.Close()

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

// splitFrontmatter splits content into frontmatter and body.
// Used for updating file frontmatter after DB update.
func splitFrontmatter(content string) (string, string) {
	if !strings.HasPrefix(content, "---\n") && content != "---" {
		return "", content
	}
	rest := strings.TrimPrefix(content, "---")
	before, after, ok := strings.Cut(rest, "\n---")
	if !ok {
		return "", content
	}
	return strings.TrimPrefix(before, "\n"), strings.TrimPrefix(after, "\n")
}
