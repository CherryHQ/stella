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
	"github.com/CherryHQ/stella/internal/cli"
	"github.com/CherryHQ/stella/internal/renderrefs"
)

func recallySaveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "save",
		Usage:     "Save already-fetched content as an article in your library",
		ArgsUsage: "<url>",
		Description: `Saves content you have ALREADY fetched — it does not fetch the URL for you.

Fetching web pages is error-prone (paywalls, JS-rendered pages, bot walls), so
that step is left to the caller (e.g. an agent using tap fetch). For a NEW
article the content is required via --content-file (or piped on stdin); without
it the server rejects the save with "content is required for new articles".

Typical workflow:
  1. Fetch the page to a file:   tap fetch "https://example.com/article" > article.md
  2. Derive title/summary/tags from that content.
  3. Save it:                    stella recally save --content-file article.md --title "..." "https://example.com/article"

Re-saving a URL that already exists WITHOUT --content-file refreshes its
metadata only (title, summary, tags) and keeps the stored content.

Examples:
  stella recally save --content-file article.md --title "Article Title" --summary "Brief summary" --tags go --tags concurrency "https://example.com/article"
  stella recally save --json --content-file tweet.md --source-type twitter --title "Tweet title" "https://x.com/user/status/123"
  cat article.md | stella recally save --title "Piped article" "https://example.com/article"

Options must be placed before the URL. Trailing options are rejected.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "canonical-url", Usage: "Canonical URL (optional, overrides computed canonical URL for deduplication)"},
			&ucli.StringFlag{Name: "title", Usage: "Article title"},
			&ucli.StringFlag{Name: "summary", Usage: "Article summary"},
			&ucli.StringSliceFlag{Name: "tags", Usage: "Article tags (can be used multiple times)"},
			&ucli.StringFlag{Name: "source-type", Usage: "Source type: web, twitter, youtube, github, rss, pdf", Value: "web"},
			&ucli.StringFlag{Name: "author", Usage: "Article author"},
			&ucli.StringFlag{Name: "content-file", Usage: "Path to file containing article content (stdin used if not provided)"},
			&ucli.StringFlag{Name: "metadata", Usage: "JSON metadata string", Value: "{}"},
			&ucli.StringFlag{Name: "published-at", Usage: "Original publication date (RFC3339)"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			if err := rejectFlagsAfterArgs(c, "stella recally save [options] <url>"); err != nil {
				return err
			}
			url := c.Args().First()
			if url == "" {
				return fmt.Errorf("usage: stella recally save [options] <url>")
			}
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
				Url:          url,
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
				// A new article with no content is the one confusing failure: the
				// command does not fetch URLs, so turn the server's terse 400 into
				// the exact fetch-then-save commands. Refreshing an existing article
				// without content succeeds (200), so it never reaches here.
				if resp.StatusCode == http.StatusBadRequest && content == "" {
					return fmt.Errorf("%q is not saved yet and no content was provided.\n\n"+
						"This command saves content you have already fetched — it does not fetch the URL.\n"+
						"Fetch the page first, then save:\n"+
						"  tap fetch %q > article.md\n"+
						"  stella recally save --content-file article.md --title \"...\" %q\n\n"+
						"Or pipe content on stdin:\n"+
						"  cat article.md | stella recally save --title \"...\" %q", url, url, url, url)
				}
				return err
			}
			created := resp.StatusCode == http.StatusCreated
			message := "Article already exists, updated metadata"
			if created {
				message = "Article saved successfully"
			}
			// Best-effort: a failed sentinel write must never fail the save.
			// `save` is upsert — only call it "created" when the article is new,
			// otherwise the card would claim a creation that didn't happen.
			saveIntent := "referenced"
			if created {
				saveIntent = "created"
			}
			_ = renderrefs.Emit(c.App.ErrWriter, renderrefs.Reference{
				Type:    "recally_article",
				ID:      article.Id,
				Intent:  saveIntent,
				Preview: &renderrefs.Preview{Title: article.Title, Status: string(article.Status)},
			})
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, article)
			}
			o := cli.Stdout(c)
			o.Printf("%s\n", message)
			o.Printf("  id:   %s\n", cli.ShortID(article.Id))
			if article.FilePath != "" {
				o.Printf("  file: %s\n", article.FilePath)
			}
			return o.Err()
		},
	}
}

func recallyListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List saved articles",
		Description: `Examples:
  stella recally list
  stella recally list --status unread --source-type twitter --limit 20 --json`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "status", Usage: "Filter by status: unread, read, archived"},
			&ucli.StringFlag{Name: "source-type", Usage: "Filter by source type: web, twitter, youtube, github, rss, pdf"},
			&ucli.BoolFlag{Name: "starred", Usage: "Show only starred articles"},
			&ucli.IntFlag{Name: "limit", Usage: "Maximum number of articles to return", Value: 50},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			params := &apiclient.ListArticlesParams{
				PageSize: apiclient.Ptr(c.Int("limit")),
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
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Articles) == 0 {
				o.Println("No articles found.")
				return o.Err()
			}
			o.Printf("Found %d article(s):\n\n", len(list.Articles))
			for _, a := range list.Articles {
				printArticleSummary(o, a, 100)
			}
			return o.Err()
		},
	}
}

func recallySearchCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "search",
		Usage:     "Search articles by title, summary, tags, or author",
		ArgsUsage: "<query>",
		Description: `Examples:
  stella recally search "concurrency patterns"
  stella recally search --limit 20 "semiconductors"`,
		Flags: []ucli.Flag{
			&ucli.IntFlag{Name: "limit", Usage: "Maximum number of results", Value: 50},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			if err := rejectFlagsAfterArgs(c, "stella recally search [options] <query>"); err != nil {
				return err
			}
			query := c.Args().First()
			if query == "" {
				return fmt.Errorf("usage: stella recally search [options] <query>")
			}
			list, err := apiclient.Call[apiclient.ArticleList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListArticles(c.Context, &apiclient.ListArticlesParams{
					Q:        &query,
					PageSize: apiclient.Ptr(c.Int("limit")),
				})
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Articles) == 0 {
				o.Println("No articles found matching your query.")
				return o.Err()
			}
			o.Printf("Found %d article(s) matching %q:\n\n", len(list.Articles), query)
			for _, a := range list.Articles {
				printArticleSummary(o, a, 80)
			}
			return o.Err()
		},
	}
}

func recallyReadCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "read",
		Usage:     "Read full article content",
		ArgsUsage: "<article-id>",
		Description: `Example:
  stella recally read <article-id>`,
		Action: func(c *ucli.Context) error {
			if err := rejectFlagsAfterArgs(c, "stella recally read <article-id>"); err != nil {
				return err
			}
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
		Description: `Examples:
  stella recally update --status read --starred <article-id>
  stella recally update --summary "New summary" --tags ai --tags infra <article-id>

Options must be placed before the article ID. Trailing options are rejected.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "status", Usage: "New status: unread, read, archived"},
			&ucli.BoolFlag{Name: "starred", Usage: "Star or unstar the article"},
			&ucli.StringFlag{Name: "summary", Usage: "New summary"},
			&ucli.StringSliceFlag{Name: "tags", Usage: "New tags (replaces existing)"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			if err := rejectFlagsAfterArgs(c, "stella recally update [options] <article-id>"); err != nil {
				return err
			}
			articleID := c.Args().First()
			if articleID == "" {
				return fmt.Errorf("usage: stella recally update [options] <article-id>")
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
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, updated)
			}
			o := cli.Stdout(c)
			o.Printf("Article %s updated successfully.\n", cli.ShortID(updated.Id))
			return o.Err()
		},
	}
}

func recallyDeleteCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "delete",
		Usage:     "Delete an article from library",
		ArgsUsage: "<article-id>",
		Description: `Example:
  stella recally delete <article-id>`,
		Flags: []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			if err := rejectFlagsAfterArgs(c, "stella recally delete [options] <article-id>"); err != nil {
				return err
			}
			articleID := c.Args().First()
			if articleID == "" {
				return fmt.Errorf("usage: stella recally delete [options] <article-id>")
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteArticle(c.Context, articleID)
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintDeleted(c, articleID)
			}
			o := cli.Stdout(c)
			o.Printf("Article %s deleted.\n", cli.ShortID(articleID))
			return o.Err()
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

func printArticleSummary(o *cli.LineWriter, a apiclient.Article, summaryWidth int) {
	starMark := " "
	if a.Starred {
		starMark = "★"
	}
	o.Printf("[%s] %s %s\n", cli.ShortID(a.Id), starMark, a.Title)
	o.Printf("    URL: %s\n", a.Url)
	if a.Summary != nil && *a.Summary != "" {
		summary := *a.Summary
		if len(summary) > summaryWidth {
			summary = summary[:summaryWidth-3] + "..."
		}
		o.Printf("    %s\n", summary)
	}
	o.Printf("    Status: %s | Source: %s | Saved: %s\n", a.Status, a.SourceType, a.SavedAt.Format("2006-01-02"))
	o.Println()
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
