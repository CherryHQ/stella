package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
)

func recallySaveCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "save",
		Usage: "Save an article to your library",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "url", Usage: "Article URL (required)", Required: true},
			&ucli.StringFlag{Name: "canonical-url", Usage: "Canonical URL (optional, overrides computed canonical URL for deduplication)"},
			&ucli.StringFlag{Name: "title", Usage: "Article title"},
			&ucli.StringFlag{Name: "summary", Usage: "Article summary"},
			&ucli.StringSliceFlag{Name: "tags", Usage: "Article tags (can be used multiple times)"},
			&ucli.StringFlag{Name: "source-type", Usage: "Source type: web, twitter, youtube, github, rss, pdf", Value: "web"},
			&ucli.StringFlag{Name: "author", Usage: "Article author"},
			&ucli.StringFlag{Name: "content-file", Usage: "Path to file containing article content (stdin used if not provided)"},
			&ucli.StringFlag{Name: "metadata", Usage: "JSON metadata string", Value: "{}"},
			&ucli.StringFlag{Name: "published-at", Usage: "Original publication date (RFC3339)"},
		},
		Action: func(c *ucli.Context) error {
			api, err := apiclient.NewAPIClient()
			if err != nil {
				return err
			}

			content, err := readContentArg(c.String("content-file"))
			if err != nil {
				return err
			}

			metadata := map[string]string{}
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

			body := apiclient.SaveArticleJSONRequestBody{
				Url:          c.String("url"),
				CanonicalUrl: optionalString(c.String("canonical-url")),
				SourceType:   sourceTypePtr(c.String("source-type")),
				Title:        optionalString(c.String("title")),
				Author:       optionalString(c.String("author")),
				Summary:      optionalString(c.String("summary")),
				Tags:         optionalStringSlice(c.StringSlice("tags")),
				Content:      optionalString(content),
				PublishedAt:  publishedAt,
			}
			if len(metadata) > 0 {
				body.Metadata = &metadata
			}

			resp, err := api.SaveArticle(c.Context, body)
			if err != nil {
				return apiclient.WrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			var article apiclient.Article
			if err := apiclient.DecodeJSON(resp, &article); err != nil {
				return err
			}
			created := resp.StatusCode == http.StatusCreated
			result := map[string]any{
				"id":        article.Id,
				"file_path": article.FilePath,
				"created":   created,
			}
			if created {
				result["message"] = "Article saved successfully"
			} else {
				result["message"] = "Article already exists, updated metadata"
			}
			return printJSON(result)
		},
	}
}

func recallyListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List saved articles",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "status", Usage: "Filter by status: unread, read, archived"},
			&ucli.StringFlag{Name: "source-type", Usage: "Filter by source type: web, twitter, youtube, github, rss, pdf"},
			&ucli.BoolFlag{Name: "starred", Usage: "Show only starred articles"},
			&ucli.IntFlag{Name: "limit", Usage: "Maximum number of articles to return", Value: 50},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			params := &apiclient.ListArticlesParams{
				Limit: apiclient.Ptr(c.Int("limit")),
			}
			if v := c.String("status"); v != "" {
				st := apiclient.ArticleStatus(v)
				params.Status = &st
			}
			if v := c.String("source-type"); v != "" {
				st := apiclient.SourceType(v)
				params.SourceType = &st
			}
			if c.IsSet("starred") {
				params.Starred = apiclient.Ptr(c.Bool("starred"))
			}
			list, err := apiclient.Call[apiclient.ArticleList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListArticles(c.Context, params)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Articles)
			}
			if len(list.Articles) == 0 {
				fmt.Println("No articles found.")
				return nil
			}
			fmt.Printf("Found %d article(s):\n\n", len(list.Articles))
			for _, a := range list.Articles {
				printArticleSummary(a, 100)
			}
			return nil
		},
	}
}

func recallySearchCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "search",
		Usage:     "Search articles by title, summary, tags, or author",
		ArgsUsage: "<query>",
		Flags: []ucli.Flag{
			&ucli.IntFlag{Name: "limit", Usage: "Maximum number of results", Value: 50},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			query := c.Args().First()
			if query == "" {
				return fmt.Errorf("usage: stella recally search <query>")
			}
			list, err := apiclient.Call[apiclient.ArticleList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListArticles(c.Context, &apiclient.ListArticlesParams{
					Q:     &query,
					Limit: apiclient.Ptr(c.Int("limit")),
				})
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Articles)
			}
			if len(list.Articles) == 0 {
				fmt.Println("No articles found matching your query.")
				return nil
			}
			fmt.Printf("Found %d article(s) matching %q:\n\n", len(list.Articles), query)
			for _, a := range list.Articles {
				printArticleSummary(a, 80)
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
				return fmt.Errorf("usage: stella recally read <article-id>")
			}
			include := "content"
			article, err := apiclient.Call[apiclient.Article](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetArticle(c.Context, articleID, &apiclient.GetArticleParams{Include: &include})
			})
			if err != nil {
				return err
			}
			if article.Content != nil && *article.Content != "" {
				fmt.Println(*article.Content)
				return nil
			}
			if article.Summary != nil {
				fmt.Println(*article.Summary)
			}
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
			&ucli.StringFlag{Name: "status", Usage: "New status: unread, read, archived"},
			&ucli.BoolFlag{Name: "starred", Usage: "Star or unstar the article"},
			&ucli.StringFlag{Name: "summary", Usage: "New summary"},
			&ucli.StringSliceFlag{Name: "tags", Usage: "New tags (replaces existing)"},
		},
		Action: func(c *ucli.Context) error {
			articleID := c.Args().First()
			if articleID == "" {
				return fmt.Errorf("usage: stella recally update <article-id>")
			}
			body := apiclient.UpdateArticleJSONRequestBody{}
			if c.IsSet("status") {
				st := apiclient.ArticleStatus(c.String("status"))
				body.Status = &st
			}
			if c.IsSet("starred") {
				body.Starred = apiclient.Ptr(c.Bool("starred"))
			}
			if c.IsSet("summary") {
				body.Summary = apiclient.Ptr(c.String("summary"))
			}
			if c.IsSet("tags") {
				tags := c.StringSlice("tags")
				body.Tags = &tags
			}
			if body.Status == nil && body.Starred == nil && body.Summary == nil && body.Tags == nil {
				return fmt.Errorf("no updates specified")
			}
			updated, err := apiclient.Call[apiclient.Article](func(api *apiclient.Client) (*http.Response, error) {
				return api.UpdateArticle(c.Context, articleID, body)
			})
			if err != nil {
				return err
			}
			fmt.Printf("Article %s updated successfully.\n", shortID(updated.Id))
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
				return fmt.Errorf("usage: stella recally delete <article-id>")
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteArticle(c.Context, articleID)
			}); err != nil {
				return err
			}
			fmt.Printf("Article %s deleted.\n", shortID(articleID))
			return nil
		},
	}
}

// readContentArg reads article content from --content-file, stdin (if piped),
// or returns "" so the server can decide between create and metadata-only
// update.
func readContentArg(contentFile string) (string, error) {
	if contentFile != "" {
		data, err := os.ReadFile(contentFile)
		if err != nil {
			return "", fmt.Errorf("read content file: %w", err)
		}
		return string(data), nil
	}
	stat, err := os.Stdin.Stat()
	if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(data), nil
	}
	return "", nil
}

func printArticleSummary(a apiclient.Article, summaryWidth int) {
	starMark := " "
	if a.Starred {
		starMark = "★"
	}
	fmt.Printf("[%s] %s %s\n", shortID(a.Id), starMark, a.Title)
	fmt.Printf("    URL: %s\n", a.Url)
	if a.Summary != nil && *a.Summary != "" {
		summary := *a.Summary
		if len(summary) > summaryWidth {
			summary = summary[:summaryWidth-3] + "..."
		}
		fmt.Printf("    %s\n", summary)
	}
	fmt.Printf("    Status: %s | Source: %s | Saved: %s\n", a.Status, a.SourceType, a.SavedAt.Format("2006-01-02"))
	fmt.Println()
}

func printJSON(v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

// shortID returns the first 8 chars of an ID for display.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func optionalStringSlice(v []string) *[]string {
	if len(v) == 0 {
		return nil
	}
	return &v
}

func sourceTypePtr(v string) *apiclient.SourceType {
	if v == "" {
		return nil
	}
	st := apiclient.SourceType(v)
	return &st
}
