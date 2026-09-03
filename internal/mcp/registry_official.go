package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/CherryHQ/stella/pkg/httpclient"
)

const (
	officialRegistryBaseURL = "https://registry.modelcontextprotocol.io"
	// registryTimeout bounds one upstream round trip; the handler layer adds a
	// context deadline on top.
	registryTimeout = 10 * time.Second
)

// RegistryRateLimitError surfaces an upstream 429 (no retry). RetryAfter is
// the upstream value when it sent one.
type RegistryRateLimitError struct{ RetryAfter string }

func (e *RegistryRateLimitError) Error() string {
	return "mcp: registry rate limited (HTTP 429)"
}

// RegistryEnvOverride is the documented base URL override.
func registryBaseURL() string {
	if v := os.Getenv("STELLA_MCP_REGISTRY_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return officialRegistryBaseURL
}

// registryRemote is one hosted endpoint of a registry server entry.
type registryRemote struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Headers []struct {
		Name        string `json:"name"`
		Value       string `json:"value"`
		Description string `json:"description"`
		IsRequired  bool   `json:"isRequired"`
		IsSecret    bool   `json:"isSecret"`
	} `json:"headers"`
}

type registryServerEntry struct {
	Server struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     string `json:"version"`
		Repository  *struct {
			URL string `json:"url"`
		} `json:"repository"`
		Remotes []registryRemote `json:"remotes"`
	} `json:"server"`
	Meta *struct {
		Official *struct {
			Status   string `json:"status"`
			IsLatest bool   `json:"isLatest"`
		} `json:"io.modelcontextprotocol.registry/official"`
	} `json:"_meta"`
}

func registryClient() *resty.Client {
	return httpclient.NewWithTimeout(registryTimeout).SetHeader("User-Agent", "stella")
}

// entryToCatalogServer converts a registry entry, keeping only streamable-http
// remotes; a stdio-only or sse-only entry yields ok=false.
func entryToCatalogServer(entry registryServerEntry) (CatalogServer, bool) {
	srv := entry.Server
	var chosen *registryRemote
	for i := range srv.Remotes {
		if srv.Remotes[i].Type == "streamable-http" {
			chosen = &srv.Remotes[i]
			break
		}
	}
	if chosen == nil || chosen.URL == "" {
		return CatalogServer{}, false
	}
	headers := make([]CatalogHeader, 0, len(chosen.Headers))
	for _, h := range chosen.Headers {
		headers = append(headers, CatalogHeader{
			Name: h.Name, Template: h.Value, Required: h.IsRequired,
			Secret: h.IsSecret, Description: h.Description,
		})
	}
	out := CatalogServer{
		Source:      RegistrySourceOfficial,
		ID:          srv.Name,
		Name:        srv.Name,
		Description: srv.Description,
		Version:     srv.Version,
		URL:         chosen.URL,
		Transport:   TransportStreamableHTTP,
		Auth:        inferAuth(headers),
		Headers:     headers,
		Repository:  repositoryURL(entry),
	}
	return out, true
}

func repositoryURL(entry registryServerEntry) string {
	if entry.Server.Repository != nil {
		return entry.Server.Repository.URL
	}
	return ""
}

func decodeRegistryEntry(raw []byte) (CatalogServer, bool, error) {
	var entry registryServerEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return CatalogServer{}, false, fmt.Errorf("mcp: decode registry server: %w", err)
	}
	out, ok := entryToCatalogServer(entry)
	return out, ok, nil
}

// officialCatalog is the only Catalog source in this phase.
type officialCatalog struct{}

// NewOfficialCatalog builds the marketplace Catalog over the official registry.
func NewOfficialCatalog() Catalog { return &officialCatalog{} }
