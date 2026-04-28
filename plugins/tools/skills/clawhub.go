package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

const (
	clawhubDefaultBaseURL = "https://clawhub.ai"
	clawhubCNMirrorURL    = "https://cn.clawhub-mirror.com"
	clawhubTimeout        = 30 * time.Second
)

type clawhubSearchResult struct {
	Score       float64 `json:"score"`
	Slug        string  `json:"slug"`
	DisplayName string  `json:"displayName"`
	Summary     string  `json:"summary,omitempty"`
	Version     *string `json:"version,omitempty"`
	UpdatedAt   int64   `json:"updatedAt,omitempty"`
}

type clawhubSkillDetail struct {
	Skill *struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"displayName"`
		Summary     string `json:"summary"`
	} `json:"skill"`
	LatestVersion *struct {
		Version string `json:"version"`
	} `json:"latestVersion"`
}

func clawhubToken() string {
	return os.Getenv("CLAWHUB_TOKEN")
}

// clawhubBaseURLs returns the ordered list of base URLs to try.
// If CLAWHUB_URL is set, only that URL is used.
// If a token is configured, only the main site is used.
// Otherwise the CN mirror is tried automatically as a fallback after 429.
func clawhubBaseURLs() []string {
	if v := os.Getenv("CLAWHUB_URL"); v != "" {
		return []string{strings.TrimRight(v, "/")}
	}
	if clawhubToken() != "" {
		return []string{clawhubDefaultBaseURL}
	}
	return []string{clawhubDefaultBaseURL, clawhubCNMirrorURL}
}

const clawhubRateLimitMsg = `ClawHub rate limit exceeded (HTTP 429). Anonymous access has a very low quota — a free API token is required.

To fix this:
1. Open https://clawhub.ai → sign up / log in (free)
2. Go to Settings → API Tokens → create a token → copy it
3. Send the following message in this chat (value is stored securely, never shown to the model):
   /config CLAWHUB_TOKEN <your-token>
4. Retry the install`

// clawhubDo performs a GET request for the given path+query, trying each base URL in order.
// On 429 from the primary site with no token, it automatically falls back to the CN mirror.
// If all URLs are rate-limited, it returns the user-facing rate-limit guidance message.
func clawhubDo(ctx context.Context, apiPath string, query url.Values) (*http.Response, error) {
	token := clawhubToken()
	client := &http.Client{Timeout: clawhubTimeout}
	rateLimited := false

	for _, base := range clawhubBaseURLs() {
		u, err := url.Parse(base + apiPath)
		if err != nil {
			return nil, err
		}
		u.RawQuery = query.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("User-Agent", "anna")

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			_ = resp.Body.Close()
			rateLimited = true
			continue
		}
		return resp, nil
	}

	if rateLimited {
		return nil, fmt.Errorf("%s", clawhubRateLimitMsg)
	}
	return nil, fmt.Errorf("clawhub: all endpoints unavailable")
}

func clawhubSearch(ctx context.Context, query string, limit int) ([]clawhubSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("limit", fmt.Sprintf("%d", limit))

	resp, err := clawhubDo(ctx, "/api/v1/search", q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clawhub search returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Results []clawhubSearchResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("clawhub search decode: %w", err)
	}
	return result.Results, nil
}

func clawhubFetchDetail(ctx context.Context, slug string) (*clawhubSkillDetail, error) {
	resp, err := clawhubDo(ctx, "/api/v1/skills/"+url.PathEscape(slug), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clawhub detail returned HTTP %d for %q", resp.StatusCode, slug)
	}
	var detail clawhubSkillDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, fmt.Errorf("clawhub detail decode: %w", err)
	}
	return &detail, nil
}

// clawhubFetchSkillFiles downloads a skill archive from clawhub.ai and returns its files.
// version may be empty; in that case the latest version is resolved first.
func clawhubFetchSkillFiles(ctx context.Context, slug, version string) (skillName string, files map[string]string, cleanup func(), err error) {
	if version == "" {
		detail, detailErr := clawhubFetchDetail(ctx, slug)
		if detailErr != nil {
			return "", nil, nil, fmt.Errorf("resolve clawhub version for %q: %w", slug, detailErr)
		}
		if detail.Skill == nil {
			return "", nil, nil, fmt.Errorf("skill %q not found on clawhub", slug)
		}
		if detail.LatestVersion == nil || detail.LatestVersion.Version == "" {
			return "", nil, nil, fmt.Errorf("skill %q has no installable version on clawhub", slug)
		}
		version = detail.LatestVersion.Version
	}

	q := url.Values{}
	q.Set("slug", slug)
	q.Set("version", version)

	resp, err := clawhubDo(ctx, "/api/v1/download", q)
	if err != nil {
		return "", nil, nil, fmt.Errorf("clawhub download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", nil, nil, fmt.Errorf("clawhub download returned HTTP %d for %q@%s", resp.StatusCode, slug, version)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, nil, fmt.Errorf("clawhub download read: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", nil, nil, fmt.Errorf("clawhub zip open for %q: %w", slug, err)
	}

	skillRoot, found := findZipSkillRoot(zr)
	if !found {
		return "", nil, nil, fmt.Errorf("clawhub archive for %q is missing SKILL.md", slug)
	}

	files = make(map[string]string)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Clean(f.Name)
		var rel string
		if skillRoot == "" {
			rel = name
		} else {
			prefix := skillRoot + "/"
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			rel = strings.TrimPrefix(name, prefix)
		}
		if rel == "" || rel == "." {
			continue
		}
		rc, openErr := f.Open()
		if openErr != nil {
			return "", nil, nil, fmt.Errorf("clawhub zip entry %q: %w", f.Name, openErr)
		}
		content, readErr := io.ReadAll(rc)
		_ = rc.Close()
		if readErr != nil {
			return "", nil, nil, fmt.Errorf("clawhub zip read %q: %w", f.Name, readErr)
		}
		files[rel] = string(content)
	}

	return slug, files, func() {}, nil
}

// findZipSkillRoot returns the directory prefix within the ZIP that contains SKILL.md,
// and whether SKILL.md was found at all. Empty prefix means SKILL.md is at the archive root.
func findZipSkillRoot(zr *zip.Reader) (prefix string, found bool) {
	for _, f := range zr.File {
		name := path.Clean(f.Name)
		if strings.EqualFold(path.Base(name), "SKILL.md") {
			dir := path.Dir(name)
			if dir == "." {
				return "", true
			}
			return dir, true
		}
	}
	return "", false
}

// parseClawhubSource parses a "clawhub:<slug>[@version]" string.
// Returns slug, version (may be empty), and whether the prefix matched.
func parseClawhubSource(source string) (slug, version string, ok bool) {
	const prefix = "clawhub:"
	if !strings.HasPrefix(strings.ToLower(source), prefix) {
		return "", "", false
	}
	spec := strings.TrimSpace(source[len(prefix):])
	if spec == "" {
		return "", "", false
	}
	idx := strings.LastIndex(spec, "@")
	if idx <= 0 {
		return spec, "", true
	}
	if idx >= len(spec)-1 {
		return "", "", false
	}
	return strings.TrimSpace(spec[:idx]), strings.TrimSpace(spec[idx+1:]), true
}
