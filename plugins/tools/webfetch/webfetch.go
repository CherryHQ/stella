package webfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"

	readability "codeberg.org/readeck/go-readability/v2"
	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/go-resty/resty/v2"

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
// directly by the server (markdown or JSON), or a readability article to be rendered.
type fetchResult struct {
	rawContent string
	article    readability.Article
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

	article, err := readability.FromReader(bytes.NewReader(body), parsed)
	if err != nil {
		return fetchResult{}, fmt.Errorf("webfetch: readability parse failed: %w", err)
	}

	if article.Node == nil {
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

func (t *WebFetchTool) render(article readability.Article, parsed *url.URL, format string) (string, error) {
	switch format {
	case formatHTML:
		return t.renderHTML(article)
	case formatText:
		return t.renderText(article)
	case formatJSON:
		return t.renderJSON(article, parsed)
	default:
		return t.renderMarkdown(article, parsed)
	}
}

func (t *WebFetchTool) renderMarkdown(article readability.Article, parsed *url.URL) (string, error) {
	var htmlBuf strings.Builder
	if err := article.RenderHTML(&htmlBuf); err != nil {
		return "", fmt.Errorf("webfetch: render html failed: %w", err)
	}

	converter := md.NewConverter(parsed.Host, true, nil)
	markdown, err := converter.ConvertString(htmlBuf.String())
	if err != nil {
		return "", fmt.Errorf("webfetch: html-to-markdown failed: %w", err)
	}

	var result strings.Builder
	t.writeMetadata(&result, article)
	result.WriteString(markdown)
	return result.String(), nil
}

func (t *WebFetchTool) renderHTML(article readability.Article) (string, error) {
	var htmlBuf strings.Builder
	if err := article.RenderHTML(&htmlBuf); err != nil {
		return "", fmt.Errorf("webfetch: render html failed: %w", err)
	}
	return htmlBuf.String(), nil
}

func (t *WebFetchTool) renderText(article readability.Article) (string, error) {
	var textBuf strings.Builder
	if err := article.RenderText(&textBuf); err != nil {
		return "", fmt.Errorf("webfetch: render text failed: %w", err)
	}

	var result strings.Builder
	if title := article.Title(); title != "" {
		fmt.Fprintf(&result, "%s\n\n", title)
	}
	result.WriteString(textBuf.String())
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

func (t *WebFetchTool) renderJSON(article readability.Article, parsed *url.URL) (string, error) {
	var textBuf strings.Builder
	if err := article.RenderText(&textBuf); err != nil {
		return "", fmt.Errorf("webfetch: render text failed: %w", err)
	}

	data := webFetchJSON{
		Title:       article.Title(),
		Author:      article.Byline(),
		Description: article.Excerpt(),
		SiteName:    article.SiteName(),
		URL:         parsed.String(),
		Content:     textBuf.String(),
	}

	b, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("webfetch: json marshal failed: %w", err)
	}
	return string(b), nil
}

// buildNoContentMessage produces a fallback message when readability could not
// extract article content (Node is nil). This commonly happens with pages that
// require JavaScript, use bot detection, or have no article-like structure
// (e.g. search engine result pages, SPAs, login walls).
func buildNoContentMessage(rawURL string, article readability.Article) string {
	var sb strings.Builder
	sb.WriteString("No readable content could be extracted from this page.\n")
	sb.WriteString("URL: " + rawURL + "\n")

	if title := article.Title(); title != "" {
		sb.WriteString("Title: " + title + "\n")
	}
	if siteName := article.SiteName(); siteName != "" {
		sb.WriteString("Site: " + siteName + "\n")
	}
	// Note: article.Excerpt() panics when Node is nil (readability bug),
	// so we only call safe metadata accessors above.

	sb.WriteString("\nThis usually means the page requires JavaScript rendering, ")
	sb.WriteString("uses bot detection, or has no article-like content (e.g. search engines, SPAs).")
	return sb.String()
}

func (t *WebFetchTool) writeMetadata(w *strings.Builder, article readability.Article) {
	if title := article.Title(); title != "" {
		fmt.Fprintf(w, "# %s\n\n", title)
	}
	if author := article.Byline(); author != "" {
		fmt.Fprintf(w, "**Author:** %s\n\n", author)
	}
}
