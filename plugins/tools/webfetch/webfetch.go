package webfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-resty/resty/v2"
	defuddle "github.com/vaayne/go-defuddle"
	"golang.org/x/net/html"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/httpclient"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

func init() {
	pkgplugins.Register("tool/webfetch", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           "tool/webfetch",
			Kind:         "tool",
			Name:         "webfetch",
			DisplayName:  "WebFetch",
			Description:  "Fetch and extract readable web page content.",
			AdminVisible: true,
			Capabilities: []string{
				pkgplugins.CapabilityTool,
			},
		})
		host.AddTool(pkgplugins.ToolSpec{
			PluginID:    "tool/webfetch",
			Name:        "webfetch",
			Description: "Fetch and extract readable web content.",
			Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
				return New(), nil
			},
		})
	}))
}

const (
	formatMarkdown = "markdown"
	formatHTML     = "html"
	formatText     = "text"
	formatJSON     = "json"

	maxBodySize = 10 * 1024 * 1024 // 10MB

)

// fetchResult holds the outcome of a fetch: either raw content served
// directly by the server (markdown or JSON), or extracted article content to be rendered.
type fetchResult struct {
	rawContent string
	article    *defuddle.Result
}

// WebFetchTool fetches a URL, extracts readable content, and returns it in the requested format.
type WebFetchTool struct {
	client *resty.Client
}

// New creates a new WebFetchTool.
func New() *WebFetchTool {
	return &WebFetchTool{
		client: httpclient.New(),
	}
}

func (t *WebFetchTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "webfetch",
		Description: "Fetch a web page and return its main content. Supports multiple output formats: markdown (default), html, text, and json.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL to fetch (http or https).",
				},
				"format": map[string]any{
					"type":        "string",
					"description": "Output format: markdown (default), html, text, or json.",
					"enum":        []string{"markdown", "html", "text", "json"},
					"default":     "markdown",
				},
			},
			"required": []string{"url"},
		},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	rawURL, ok := args["url"].(string)
	if !ok || rawURL == "" {
		return "", fmt.Errorf("webfetch: url is required")
	}

	format, _ := args["format"].(string)
	if format == "" {
		format = formatMarkdown
	}
	switch format {
	case formatMarkdown, formatHTML, formatText, formatJSON:
	default:
		format = formatMarkdown
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("webfetch: invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("webfetch: unsupported scheme %q (only http/https)", parsed.Scheme)
	}

	result, err := t.fetch(ctx, rawURL, parsed, format)
	if err != nil {
		return "", err
	}

	// Server returned the requested format directly — use it as-is.
	if result.rawContent != "" {
		return tools.TruncateHead(result.rawContent).Content, nil
	}

	content, err := t.render(result.article, parsed, format)
	if err != nil {
		return "", err
	}

	return tools.TruncateHead(content).Content, nil
}

// acceptHeader returns the Accept header value based on the requested format.
func acceptHeader(format string) string {
	switch format {
	case formatJSON:
		return "application/json, text/html;q=0.9, */*;q=0.8"
	default:
		return "text/markdown, text/html;q=0.9, */*;q=0.8"
	}
}

func (t *WebFetchTool) fetch(ctx context.Context, rawURL string, parsed *url.URL, format string) (fetchResult, error) {
	// URL parsing and tool hooks can outlive the ownership check at dispatch.
	// Revalidate at the actual network boundary so a stale executor cannot start
	// a new request. A transport error remains outcome-unknown and is not retried.
	if err := agentrun.Check(ctx); err != nil {
		return fetchResult{}, err
	}
	resp, err := t.client.R().
		SetContext(ctx).
		SetHeader("User-Agent", "Mozilla/5.0 (compatible; Stella/1.0)").
		SetHeader("Accept", acceptHeader(format)).
		Get(rawURL)
	if err != nil {
		return fetchResult{}, fmt.Errorf("webfetch: %w", err)
	}

	if resp.StatusCode() >= http.StatusBadRequest {
		return fetchResult{}, fmt.Errorf("webfetch: HTTP %d %s", resp.StatusCode(), resp.Status())
	}

	body := resp.Body()
	if len(body) > maxBodySize {
		body = body[:maxBodySize]
	}
	mediaType := parseMediaType(resp.Header().Get("Content-Type"))

	// If the server returned the preferred format directly, use it without conversion.
	if (format == formatMarkdown && mediaType == "text/markdown") ||
		(format == formatJSON && mediaType == "application/json") {
		return fetchResult{rawContent: string(body)}, nil
	}

	parser, err := defuddle.NewParser()
	if err != nil {
		return fetchResult{}, fmt.Errorf("webfetch: defuddle init failed: %w", err)
	}
	defer parser.Close()

	article, err := parser.Parse(string(body), parsed.String(), &defuddle.Options{Markdown: format == formatMarkdown})
	if err != nil {
		return fetchResult{}, fmt.Errorf("webfetch: defuddle parse failed: %w", err)
	}

	if strings.TrimSpace(article.Content) == "" || (article.WordCount == 0 && htmlToText(article.Content) == "") {
		return fetchResult{rawContent: buildNoContentMessage(rawURL, article)}, nil
	}

	return fetchResult{article: article}, nil
}

// parseMediaType extracts the media type from a Content-Type header.
func parseMediaType(contentType string) string {
	if contentType == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return mediaType
}

func (t *WebFetchTool) render(article *defuddle.Result, parsed *url.URL, format string) (string, error) {
	switch format {
	case formatHTML:
		return t.renderHTML(article)
	case formatText:
		return t.renderText(article)
	case formatJSON:
		return t.renderJSON(article, parsed)
	default:
		return t.renderMarkdown(article)
	}
}

func (t *WebFetchTool) renderMarkdown(article *defuddle.Result) (string, error) {
	var result strings.Builder
	t.writeMetadata(&result, article)
	result.WriteString(article.Markdown)
	return result.String(), nil
}

func (t *WebFetchTool) renderHTML(article *defuddle.Result) (string, error) {
	return article.Content, nil
}

func (t *WebFetchTool) renderText(article *defuddle.Result) (string, error) {
	var result strings.Builder
	if article.Title != "" {
		fmt.Fprintf(&result, "%s\n\n", article.Title)
	}
	result.WriteString(htmlToText(article.Content))
	return result.String(), nil
}

type webFetchJSON struct {
	Title       string `json:"title,omitempty"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	URL         string `json:"url"`
	Content     string `json:"content"`
}

func (t *WebFetchTool) renderJSON(article *defuddle.Result, parsed *url.URL) (string, error) {
	data := webFetchJSON{
		Title:       article.Title,
		Author:      article.Author,
		Description: article.Description,
		SiteName:    article.Site,
		URL:         parsed.String(),
		Content:     htmlToText(article.Content),
	}

	b, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("webfetch: json marshal failed: %w", err)
	}
	return string(b), nil
}

// buildNoContentMessage produces a fallback message when defuddle could not
// extract article content. This commonly happens with pages that require
// JavaScript, use bot detection, or have no article-like structure (e.g. search
// engine result pages, SPAs, login walls).
func buildNoContentMessage(rawURL string, article *defuddle.Result) string {
	var sb strings.Builder
	sb.WriteString("No readable content could be extracted from this page.\n")
	sb.WriteString("URL: " + rawURL + "\n")

	if article != nil {
		if article.Title != "" {
			sb.WriteString("Title: " + article.Title + "\n")
		}
		if article.Site != "" {
			sb.WriteString("Site: " + article.Site + "\n")
		}
	}

	sb.WriteString("\nThis usually means the page requires JavaScript rendering, ")
	sb.WriteString("uses bot detection, or has no article-like content (e.g. search engines, SPAs).")
	return sb.String()
}

func htmlToText(content string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(content))
	var text strings.Builder
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if !errors.Is(tokenizer.Err(), io.EOF) {
				return strings.Join(strings.Fields(text.String()), " ")
			}
			return strings.Join(strings.Fields(text.String()), " ")
		case html.TextToken:
			text.WriteByte(' ')
			text.Write(tokenizer.Text())
		}
	}
}

func (t *WebFetchTool) writeMetadata(w *strings.Builder, article *defuddle.Result) {
	if article.Title != "" {
		fmt.Fprintf(w, "# %s\n\n", article.Title)
	}
	if article.Author != "" {
		fmt.Fprintf(w, "**Author:** %s\n\n", article.Author)
	}
}
