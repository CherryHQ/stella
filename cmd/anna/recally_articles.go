package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	ucli "github.com/urfave/cli/v2"
	recallyclient "github.com/vaayne/anna/pkg/recally/client"
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
			api, err := recallyAPI()
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

			body := recallyclient.SaveArticleJSONRequestBody{
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
				return wrapServerErr(err)
			}
			var article recallyclient.Article
			if err := recallyclient.DecodeJSON(resp, &article); err != nil {
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
			api, err := recallyAPI()
			if err != nil {
				return err
			}
			params := &recallyclient.ListArticlesParams{
				Limit: recallyclient.Ptr(c.Int("limit")),
			}
			if v := c.String("status"); v != "" {
				st := recallyclient.ArticleStatus(v)
				params.Status = &st
			}
			if v := c.String("source-type"); v != "" {
				st := recallyclient.SourceType(v)
				params.SourceType = &st
			}
			if c.IsSet("starred") {
				params.Starred = recallyclient.Ptr(c.Bool("starred"))
			}
			resp, err := api.ListArticles(c.Context, params)
			if err != nil {
				return wrapServerErr(err)
			}
			var list recallyclient.ArticleList
			if err := recallyclient.DecodeJSON(resp, &list); err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Items)
			}
			if len(list.Items) == 0 {
				fmt.Println("No articles found.")
				return nil
			}
			fmt.Printf("Found %d article(s):\n\n", len(list.Items))
			for _, a := range list.Items {
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
				return fmt.Errorf("usage: anna recally search <query>")
			}
			api, err := recallyAPI()
			if err != nil {
				return err
			}
			resp, err := api.ListArticles(c.Context, &recallyclient.ListArticlesParams{
				Q:     &query,
				Limit: recallyclient.Ptr(c.Int("limit")),
			})
			if err != nil {
				return wrapServerErr(err)
			}
			var list recallyclient.ArticleList
			if err := recallyclient.DecodeJSON(resp, &list); err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Items)
			}
			if len(list.Items) == 0 {
				fmt.Println("No articles found matching your query.")
				return nil
			}
			fmt.Printf("Found %d article(s) matching %q:\n\n", len(list.Items), query)
			for _, a := range list.Items {
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
				return fmt.Errorf("usage: anna recally read <article-id>")
			}
			api, err := recallyAPI()
			if err != nil {
				return err
			}
			include := "content"
			resp, err := api.GetArticle(c.Context, articleID, &recallyclient.GetArticleParams{Include: &include})
			if err != nil {
				return wrapServerErr(err)
			}
			var article recallyclient.Article
			if err := recallyclient.DecodeJSON(resp, &article); err != nil {
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
				return fmt.Errorf("usage: anna recally update <article-id>")
			}
			api, err := recallyAPI()
			if err != nil {
				return err
			}
			body := recallyclient.UpdateArticleJSONRequestBody{}
			if c.IsSet("status") {
				st := recallyclient.ArticleStatus(c.String("status"))
				body.Status = &st
			}
			if c.IsSet("starred") {
				body.Starred = recallyclient.Ptr(c.Bool("starred"))
			}
			if c.IsSet("summary") {
				body.Summary = recallyclient.Ptr(c.String("summary"))
			}
			if c.IsSet("tags") {
				tags := c.StringSlice("tags")
				body.Tags = &tags
			}
			if body.Status == nil && body.Starred == nil && body.Summary == nil && body.Tags == nil {
				return fmt.Errorf("no updates specified")
			}
			resp, err := api.UpdateArticle(c.Context, articleID, body)
			if err != nil {
				return wrapServerErr(err)
			}
			var updated recallyclient.Article
			if err := recallyclient.DecodeJSON(resp, &updated); err != nil {
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
				return fmt.Errorf("usage: anna recally delete <article-id>")
			}
			api, err := recallyAPI()
			if err != nil {
				return err
			}
			resp, err := api.DeleteArticle(c.Context, articleID)
			if err != nil {
				return wrapServerErr(err)
			}
			if err := recallyclient.DecodeJSON(resp, nil); err != nil {
				return err
			}
			fmt.Printf("Article %s deleted.\n", shortID(articleID))
			return nil
		},
	}
}

// ----------------------------- shared CLI helpers ---------------------------

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

func printArticleSummary(a recallyclient.Article, summaryWidth int) {
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

func sourceTypePtr(v string) *recallyclient.SourceType {
	if v == "" {
		return nil
	}
	st := recallyclient.SourceType(v)
	return &st
}

// wrapServerErr decorates connection errors with a hint about ANNA_SERVER_URL
// so users immediately understand whether the server is reachable.
func wrapServerErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("call anna server: %w (run 'anna serve' or set ANNA_SERVER_URL)", err)
}
