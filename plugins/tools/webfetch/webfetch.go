package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

func init() {
	plugintools.Register("webfetch", func() tools.Tool { return New() })
}

const (
	formatMarkdown = "markdown"
	formatHTML     = "html"
	formatText     = "text"
	formatJSON     = "json"

	maxBodySize = 10 * 1024 * 1024 // 10MB

	defaultMaxLines = 2000
	defaultMaxBytes = 50 * 1024 // 50KB
)

// fetchResult holds the outcome of a fetch: either raw content served
// directly by the server (markdown or JSON), or a readability article to be rendered.
type fetchResult struct {
	rawContent string
	article    readability.Article
}

// WebFetchTool fetches a URL, extracts readable content, and returns it in the requested format.
type WebFetchTool struct {
	client *http.Client
}

// New creates a new WebFetchTool.
func New() *WebFetchTool {
	return &WebFetchTool{
		client: &http.Client{Timeout: 30 * time.Second},
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
		return truncateHead(result.rawContent), nil
	}

	content, err := t.render(result.article, parsed, format)
	if err != nil {
		return "", err
	}

	return truncateHead(content), nil
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetchResult{}, fmt.Errorf("webfetch: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Anna/1.0)")
	req.Header.Set("Accept", acceptHeader(format))

	resp, err := t.client.Do(req)
	if err != nil {
		return fetchResult{}, fmt.Errorf("webfetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return fetchResult{}, fmt.Errorf("webfetch: HTTP %d %s", resp.StatusCode, resp.Status)
	}

	body := io.LimitReader(resp.Body, maxBodySize)
	mediaType := parseMediaType(resp.Header.Get("Content-Type"))

	// If the server returned the preferred format directly, use it without conversion.
	if (format == formatMarkdown && mediaType == "text/markdown") ||
		(format == formatJSON && mediaType == "application/json") {
		data, err := io.ReadAll(body)
		if err != nil {
			return fetchResult{}, fmt.Errorf("webfetch: read body: %w", err)
		}
		return fetchResult{rawContent: string(data)}, nil
	}

	article, err := readability.FromReader(body, parsed)
	if err != nil {
		return fetchResult{}, fmt.Errorf("webfetch: readability parse failed: %w", err)
	}

	if article.Node == nil {
		// Readability parsed the HTML but found no extractable content.
		// Return whatever metadata was found so the LLM has something useful.
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

// truncateHead keeps the first N lines / bytes (whichever limit is hit first).
// This is a self-contained version to avoid circular imports with internal/agent/tool.
func truncateHead(output string) string {
	lineLimit := maxLinesEnv()
	byteLimit := maxBytesEnv()

	if len(output) <= byteLimit {
		lines := strings.SplitAfter(output, "\n")
		if len(lines) <= lineLimit {
			return output
		}
	}

	// Truncate by keeping the first lines within both limits.
	lines := strings.SplitAfter(output, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var kept []string
	keptBytes := 0
	for _, line := range lines {
		if len(kept) >= lineLimit || keptBytes+len(line) > byteLimit {
			break
		}
		kept = append(kept, line)
		keptBytes += len(line)
	}

	if len(kept) == len(lines) {
		return output
	}

	return fmt.Sprintf("[Output truncated — showing first %d of %d lines (%d bytes total)]\n\n%s\n...",
		len(kept), len(lines), len(output), strings.Join(kept, ""))
}

func maxLinesEnv() int {
	if v := os.Getenv("ANNA_TOOL_MAX_LINES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxLines
}

func maxBytesEnv() int {
	if v := os.Getenv("ANNA_TOOL_MAX_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxBytes
}
