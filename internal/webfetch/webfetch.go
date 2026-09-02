// Package webfetch fetches a public web page through the server's egress
// policy and extracts its readable content. It serves server-side consumers
// such as Recally; the model reads the web through the `web` skill instead.
package webfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	defuddle "github.com/vaayne/go-defuddle"
	xhtml "golang.org/x/net/html"
)

const (
	maxBodySize       = 10 * 1024 * 1024 // 10MB
	fetchTimeout      = 30 * time.Second
	jinaReaderBaseURL = "https://r.jina.ai/"
)

var errNoReadableContent = errors.New("no readable content")

// fetchResult holds either raw content or an extracted article.
// A nil article means raw content, including a valid empty response.
type fetchResult struct {
	content string
	article *defuddle.Result
}

func rawFetchResult(content string) fetchResult { return fetchResult{content: content} }

// publicClient applies the public-egress policy and reuses connections.
var publicClient = newPublicClient(fetchTimeout)

// Article is a readable page extracted server-side, for consumers such as
// Recally that must store a body without routing it through the model.
// Metadata fields are empty when the source was not an HTML article.
type Article struct {
	URL         string
	Title       string
	Author      string
	Description string
	Site        string
	Published   string
	Markdown    string
}

// Extract fetches one public URL through the egress policy and returns its
// readable Markdown. It fails when no readable content exists.
func Extract(ctx context.Context, rawURL string) (Article, error) {
	parsed, err := parseFetchURL(rawURL)
	if err != nil {
		return Article{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	result, err := fetchWithFallback(ctx, publicClient, parsed)
	if err != nil {
		return Article{}, err
	}
	out := Article{URL: parsed.String(), Markdown: result.content}
	if result.article != nil {
		out.Title = result.article.Title
		out.Author = result.article.Author
		out.Description = result.article.Description
		out.Site = result.article.Site
		out.Published = result.article.Published
		out.Markdown = result.article.Markdown
	}
	return out, nil
}

func parseFetchURL(rawURL string) (*url.URL, error) {
	if rawURL == "" || len([]rune(rawURL)) > 4096 {
		return nil, errors.New("web_fetch: url must contain 1 to 4096 characters")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("web_fetch: unsupported scheme %q (only http/https)", parsed.Scheme)
	}
	return parsed, nil
}

// fetchWithFallback tries the direct fetch first and Jina Reader second. A
// direct extraction miss still carries the page's title and site, so it is
// kept when Jina cannot do better instead of reporting both failures.
func fetchWithFallback(ctx context.Context, client *http.Client, parsed *url.URL) (fetchResult, error) {
	result, err := fetchPage(ctx, client, parsed)
	if err == nil || validatePublicReaderTarget(ctx, parsed) != nil {
		return result, err
	}
	jinaResult, jinaErr := fetchJinaReader(ctx, client, parsed)
	if jinaErr == nil {
		return jinaResult, nil
	}
	if errors.Is(err, errNoReadableContent) {
		return result, err
	}
	return fetchResult{}, errors.Join(err, fmt.Errorf("web_fetch: Jina Reader fallback: %w", jinaErr))
}

// fetchPage relies on the client's public-egress transport for URL validation;
// there is no separate pre-check.
func fetchPage(ctx context.Context, client *http.Client, parsed *url.URL) (fetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return fetchResult{}, errors.New("web_fetch: could not create request")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Stella/1.0)")
	req.Header.Set("Accept", "text/markdown, text/html;q=0.9, */*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return fetchResult{}, fmt.Errorf("web_fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		return fetchResult{}, fmt.Errorf("web_fetch: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBodySize {
		return fetchResult{}, fmt.Errorf("web_fetch: response body exceeds %d MB limit", maxBodySize/(1024*1024))
	}
	mediaType := parseMediaType(resp.Header.Get("Content-Type"))
	if !allowedMediaType(mediaType) {
		return fetchResult{}, fmt.Errorf("web_fetch: unsupported content type %q", mediaType)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return fetchResult{}, fmt.Errorf("web_fetch: read response: %w", err)
	}
	if len(body) > maxBodySize {
		return fetchResult{}, fmt.Errorf("web_fetch: response body exceeds %d MB limit", maxBodySize/(1024*1024))
	}
	// A source document that is already plain text, markdown, or JSON is not an
	// HTML article, so retain it verbatim rather than making the extractor guess.
	if mediaType == "text/markdown" || mediaType == "text/plain" || mediaType == "application/json" {
		return rawFetchResult(string(body)), nil
	}

	parser, err := defuddle.NewParser()
	if err != nil {
		return fetchResult{}, fmt.Errorf("web_fetch: defuddle init failed: %w", err)
	}
	defer parser.Close()

	article, err := parser.Parse(string(body), parsed.String(), &defuddle.Options{Markdown: true})
	if err != nil {
		return fetchResult{}, fmt.Errorf("web_fetch: defuddle parse failed: %w", err)
	}

	if strings.TrimSpace(article.Content) == "" || (article.WordCount == 0 && htmlToText(article.Content) == "") {
		return fetchResult{article: article}, errNoReadableContent
	}

	return fetchResult{article: article}, nil
}

func validatePublicReaderTarget(ctx context.Context, target *url.URL) error {
	if err := validatePublicURL(target); err != nil {
		return err
	}
	_, err := resolvePublicHost(ctx, target.Hostname())
	return err
}

func fetchJinaReader(ctx context.Context, client *http.Client, target *url.URL) (fetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jinaReaderBaseURL+target.String(), nil)
	if err != nil {
		return fetchResult{}, errors.New("could not create request")
	}
	req.Header.Set("Accept", "text/markdown")
	req.Header.Set("X-No-Cache", "true")
	resp, err := client.Do(req)
	if err != nil {
		return fetchResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fetchResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return fetchResult{}, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxBodySize {
		return fetchResult{}, fmt.Errorf("response body exceeds %d MB limit", maxBodySize/(1024*1024))
	}
	const marker = "Markdown Content:"
	_, content, ok := strings.Cut(string(body), marker)
	if !ok || strings.TrimSpace(content) == "" {
		return fetchResult{}, errors.New("response has no markdown content")
	}
	return rawFetchResult(strings.TrimSpace(content)), nil
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
	return strings.ToLower(mediaType)
}

func allowedMediaType(mediaType string) bool {
	switch mediaType {
	case "text/html", "application/xhtml+xml", "text/plain", "text/markdown", "application/json":
		return true
	default:
		return false
	}
}

func htmlToText(content string) string {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(content))
	var text strings.Builder
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			return strings.Join(strings.Fields(text.String()), " ")
		case xhtml.TextToken:
			text.WriteByte(' ')
			text.Write(tokenizer.Text())
		}
	}
}
