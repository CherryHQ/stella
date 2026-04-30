package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/recally"
)

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
			store, userID, dbConn, err := openRecally(c.Context)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := dbConn.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			var content string
			contentFile := c.String("content-file")
			if contentFile != "" {
				data, err := os.ReadFile(contentFile)
				if err != nil {
					return fmt.Errorf("read content file: %w", err)
				}
				content = string(data)
			} else {
				stat, err := os.Stdin.Stat()
				if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
					data, err := io.ReadAll(os.Stdin)
					if err != nil {
						return fmt.Errorf("read stdin: %w", err)
					}
					content = string(data)
				}
			}

			metadata := make(map[string]string)
			if metaStr := c.String("metadata"); metaStr != "" && metaStr != "{}" {
				if err := json.Unmarshal([]byte(metaStr), &metadata); err != nil {
					return fmt.Errorf("parse metadata JSON: %w", err)
				}
			}

			var publishedAt *time.Time
			if pubStr := c.String("published-at"); pubStr != "" {
				t, err := time.Parse(time.RFC3339, pubStr)
				if err != nil {
					return fmt.Errorf("parse published-at: %w", err)
				}
				publishedAt = &t
			}

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

			annaHome := config.AnnaHome()
			fm := recally.NewFileManager(annaHome)
			filePath := fm.ArticlePath(userID, article.Title, article.SavedAt)
			if err := fm.WriteArticle(filePath, article, content); err != nil {
				return fmt.Errorf("write article file: %w", err)
			}

			relPath := fm.RelativePath(filePath)
			if err := store.UpdateArticleFilePath(c.Context, article.ID, relPath); err != nil {
				return fmt.Errorf("update file path: %w", err)
			}
			article.FilePath = relPath

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
			out, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal result: %w", err)
			}
			fmt.Println(string(out))
			return nil
		},
	}
}

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
			store, userID, dbConn, err := openRecally(c.Context)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := dbConn.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

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
				out, err := json.MarshalIndent(articles, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal articles: %w", err)
				}
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

			store, userID, dbConn, err := openRecally(c.Context)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := dbConn.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			articles, err := store.SearchArticles(c.Context, userID, query, c.Int("limit"))
			if err != nil {
				return fmt.Errorf("search articles: %w", err)
			}

			if c.Bool("json") {
				out, err := json.MarshalIndent(articles, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal articles: %w", err)
				}
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

			store, userID, dbConn, err := openRecally(c.Context)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := dbConn.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			article, err := store.GetArticle(c.Context, articleID)
			if err != nil {
				return err
			}

			if article.UserID != userID {
				return fmt.Errorf("article not found or access denied")
			}

			annaHome := config.AnnaHome()
			fm := recally.NewFileManager(annaHome)
			filePath := article.FilePath
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(annaHome, filePath)
			}

			content, err := fm.ReadArticleFull(filePath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not read file: %v\n", err)
				content = article.Summary
			}

			fmt.Println(content)
			return nil
		},
	}
}

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

			store, userID, dbConn, err := openRecally(c.Context)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := dbConn.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

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

			annaHome := config.AnnaHome()
			fm := recally.NewFileManager(annaHome)
			filePath := updated.FilePath
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(annaHome, filePath)
			}

			if content, err := fm.ReadArticleFull(filePath); err == nil {
				_, body := recally.SplitFrontmatter(content)
				if err := fm.WriteArticle(filePath, updated, strings.TrimSpace(body)); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update file frontmatter: %v\n", err)
				}
			}

			fmt.Printf("Article %s updated successfully.\n", articleID[:8])
			return nil
		},
	}
}

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

			store, userID, dbConn, err := openRecally(c.Context)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := dbConn.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			article, err := store.GetArticle(c.Context, articleID)
			if err != nil {
				return err
			}
			if article.UserID != userID {
				return fmt.Errorf("article not found or access denied")
			}

			if err := store.DeleteArticle(c.Context, articleID); err != nil {
				return fmt.Errorf("delete article: %w", err)
			}

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
