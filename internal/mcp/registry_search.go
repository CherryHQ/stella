package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// pageTokenPrefix separates the source from the upstream cursor. An
// unprefixed token is accepted as a bare cursor for compatibility with
// callers that treat tokens as opaque strings.
const pageTokenPrefix = RegistrySourceOfficial + ":"

func encodePageToken(cursor string) string {
	if cursor == "" {
		return ""
	}
	return pageTokenPrefix + cursor
}

func decodePageToken(token string) (source, cursor string, err error) {
	if token == "" {
		return RegistrySourceOfficial, "", nil
	}
	source, cursor, ok := strings.Cut(token, ":")
	if !ok || source != RegistrySourceOfficial {
		return "", "", fmt.Errorf("mcp: invalid page token %q", token)
	}
	return source, cursor, nil
}

// Search pages the upstream registry, keeping only streamable-http entries,
// until pageSize matches are collected, upstream is exhausted, or
// maxUpstreamPagesPerRequest upstream pages were consumed for this request.
func (c *officialCatalog) Search(ctx context.Context, q string, pageSize int, pageToken string) (CatalogPage, error) {
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 50 {
		pageSize = 50
	}
	_, cursor, err := decodePageToken(pageToken)
	if err != nil {
		return CatalogPage{}, err
	}

	out := make([]CatalogServer, 0, pageSize)
	hasMore := false
	for range maxUpstreamPagesPerRequest {
		entries, next, err := c.fetchPage(ctx, q, pageSize, cursor)
		if err != nil {
			return CatalogPage{}, err
		}
		for _, entry := range entries {
			if s, ok := entryToCatalogServer(entry); ok {
				out = append(out, s)
			}
		}
		if len(out) >= pageSize {
			// Page budget filled; keep serving only if upstream has more.
			hasMore = next != ""
			cursor = next
			break
		}
		if next == "" {
			hasMore = false
			break
		}
		cursor, hasMore = next, true
	}
	// No truncation: the cursor can only resume at an upstream page boundary,
	// so cutting the slice to pageSize would silently drop the tail. A page is
	// therefore at least pageSize matches (unless upstream is exhausted or the
	// page cap hit) and at most pageSize plus one upstream page.
	out = dedupeServers(out)
	next := ""
	if hasMore {
		next = encodePageToken(cursor)
	}
	return CatalogPage{Servers: out, NextPageToken: next}, nil
}

// fetchPage performs one upstream list round trip.
func (c *officialCatalog) fetchPage(ctx context.Context, q string, limit int, cursor string) ([]registryServerEntry, string, error) {
	req := registryClient().R().SetContext(ctx).
		SetQueryParam("limit", fmt.Sprintf("%d", limit))
	if q != "" {
		req = req.SetQueryParam("search", q)
	}
	if cursor != "" {
		req = req.SetQueryParam("cursor", cursor)
	}
	resp, err := req.Get(registryBaseURL() + "/v0/servers")
	if err != nil {
		return nil, "", fmt.Errorf("mcp: registry list: %w", err)
	}
	if resp.StatusCode() == 429 {
		return nil, "", &RegistryRateLimitError{RetryAfter: resp.Header().Get("Retry-After")}
	}
	if resp.IsError() {
		return nil, "", fmt.Errorf("mcp: registry list returned HTTP %d", resp.StatusCode())
	}
	// Decode explicitly: resty's SetResult swallows malformed-JSON errors.
	var payload struct {
		Servers  []registryServerEntry `json:"servers"`
		Metadata *struct {
			NextCursor string `json:"nextCursor"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Body(), &payload); err != nil {
		return nil, "", fmt.Errorf("mcp: decode registry list: %w", err)
	}
	next := ""
	if payload.Metadata != nil {
		next = payload.Metadata.NextCursor
	}
	return payload.Servers, next, nil
}

// Get resolves one registry server by id. The versions-list endpoint is used
// (it covers every version); the entry flagged isLatest wins, falling back to
// the first entry.
func (c *officialCatalog) Get(ctx context.Context, source, id string) (CatalogServer, error) {
	if source != RegistrySourceOfficial {
		return CatalogServer{}, fmt.Errorf("mcp: unknown registry source %q", source)
	}
	if strings.TrimSpace(id) == "" {
		return CatalogServer{}, fmt.Errorf("mcp: registry server id is required")
	}
	path := "/v0/servers/" + url.PathEscape(id) + "/versions"
	resp, err := registryClient().R().SetContext(ctx).Get(registryBaseURL() + path)
	if err != nil {
		return CatalogServer{}, fmt.Errorf("mcp: registry detail: %w", err)
	}
	if resp.StatusCode() == 429 {
		return CatalogServer{}, &RegistryRateLimitError{RetryAfter: resp.Header().Get("Retry-After")}
	}
	if resp.StatusCode() == 404 {
		return CatalogServer{}, fmt.Errorf("mcp: registry server %q not found", id)
	}
	if resp.IsError() {
		return CatalogServer{}, fmt.Errorf("mcp: registry detail returned HTTP %d", resp.StatusCode())
	}
	var payload struct {
		Servers []json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(resp.Body(), &payload); err != nil {
		return CatalogServer{}, fmt.Errorf("mcp: decode registry detail: %w", err)
	}
	var fallback *CatalogServer
	for _, raw := range payload.Servers {
		entry, ok, err := decodeRegistryEntry(raw)
		if err != nil || !ok {
			continue
		}
		if isLatestEntry(raw) {
			return entry, nil
		}
		if fallback == nil {
			fallback = &entry
		}
	}
	if fallback != nil {
		return *fallback, nil
	}
	return CatalogServer{}, fmt.Errorf("mcp: registry server %q has no streamable-http remote", id)
}

// isLatestEntry reads the official registry's isLatest flag.
func isLatestEntry(raw json.RawMessage) bool {
	var entry registryServerEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return false
	}
	return entry.Meta != nil && entry.Meta.Official != nil && entry.Meta.Official.IsLatest
}
