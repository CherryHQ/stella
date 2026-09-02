package skill

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/CherryHQ/stella/pkg/httpclient"
)

const (
	clawhubDefaultBaseURL = "https://clawhub.ai"
	clawhubCNMirrorURL    = "https://cn.clawhub-mirror.com"
)

type clawhubSearchResult struct {
	Score       float64 `json:"score"`
	Slug        string  `json:"slug"`
	DisplayName string  `json:"displayName"`
	Summary     string  `json:"summary,omitempty"`
	Version     *string `json:"version,omitempty"`
	UpdatedAt   int64   `json:"updatedAt,omitempty"`
	OwnerHandle string  `json:"ownerHandle,omitempty"`
	Owner       *struct {
		Handle      string `json:"handle"`
		DisplayName string `json:"displayName"`
		Image       string `json:"image"`
	} `json:"owner,omitempty"`
}

// clawhubListItem is one row from GET /api/v1/skills (browse/popular endpoint).
type clawhubListItem struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	Summary     string `json:"summary,omitempty"`
	Tags        struct {
		Latest string `json:"latest"`
	} `json:"tags"`
	Stats struct {
		Downloads       int `json:"downloads"`
		InstallsCurrent int `json:"installsCurrent"`
		InstallsAllTime int `json:"installsAllTime"`
		Stars           int `json:"stars"`
	} `json:"stats"`
	UpdatedAt     int64 `json:"updatedAt,omitempty"`
	LatestVersion struct {
		Version string `json:"version"`
	} `json:"latestVersion"`
}

// CatalogSkill is one marketplace row from the ClawHub registry.
type CatalogSkill struct {
	Slug         string
	Name         string
	Summary      string
	Version      string    // empty when unknown
	Downloads    *int      // nil in search mode (upstream search has no stats)
	Installs     *int      // nil in search mode
	UpdatedAt    time.Time // zero when unknown
	AuthorHandle string    // empty in browse mode (upstream list has no owner)
	AuthorImage  string
}

// CatalogSkillDetail is a single marketplace skill enriched with its README and file list,
// fetched by downloading the skill archive from ClawHub.
type CatalogSkillDetail struct {
	Slug    string
	Name    string
	Summary string
	Version string
	Readme  string   // SKILL.md content, empty when absent
	Files   []string // relative file paths, sorted
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

func newClawhubClient() *resty.Client {
	client := httpclient.New().SetHeader("User-Agent", "stella")
	if token := clawhubToken(); token != "" {
		client.SetAuthToken(token)
	}
	return client
}

// clawhubWithFallback calls fn(base) for each base URL in order, stopping on the first
// success. fn returns (is429, err): is429=true signals a rate-limit so the next URL is
// tried; any other error stops immediately. If all URLs return 429, the rate-limit
// guidance message is returned.
func clawhubWithFallback(fn func(base string) (is429 bool, err error)) error {
	rateLimited := false
	for _, base := range clawhubBaseURLs() {
		is429, err := fn(base)
		if is429 {
			rateLimited = true
			continue
		}
		return err
	}
	if rateLimited {
		return fmt.Errorf("%s", clawhubRateLimitMsg)
	}
	return fmt.Errorf("clawhub: all endpoints unavailable")
}

// clawhubListSkills fetches popular skills from GET /api/v1/skills sorted by downloads.
// cursor is passed through as-is; an empty string omits the query parameter.
// Returns the items, the opaque next cursor (empty on last page), and any error.
func clawhubListSkills(ctx context.Context, limit int, cursor string) ([]clawhubListItem, string, error) {
	if limit <= 0 {
		limit = 20
	}
	client := newClawhubClient()
	var result struct {
		Items      []clawhubListItem `json:"items"`
		NextCursor string            `json:"nextCursor"`
	}
	err := clawhubWithFallback(func(base string) (bool, error) {
		req := client.R().
			SetContext(ctx).
			SetQueryParam("sort", "downloads").
			SetQueryParam("limit", fmt.Sprintf("%d", limit)).
			SetResult(&result)
		if cursor != "" {
			req = req.SetQueryParam("cursor", cursor)
		}
		resp, err := req.Get(base + "/api/v1/skills")
		if err != nil {
			return false, err
		}
		if resp.StatusCode() == 429 {
			return true, nil
		}
		if resp.IsError() {
			return false, fmt.Errorf("clawhub list returned HTTP %d", resp.StatusCode())
		}
		return false, nil
	})
	if err != nil {
		return nil, "", err
	}
	return result.Items, result.NextCursor, nil
}

// BrowseCatalog returns popular skills when q is empty (paginated via the
// opaque pageToken) and search results when q is set (no pagination).
func BrowseCatalog(ctx context.Context, q string, limit int, pageToken string) (items []CatalogSkill, nextPageToken string, err error) {
	if q != "" {
		// Search mode: use clawhubSearch, no pagination.
		results, searchErr := clawhubSearch(ctx, q, limit)
		if searchErr != nil {
			return nil, "", searchErr
		}
		items = make([]CatalogSkill, 0, len(results))
		for _, r := range results {
			var version string
			if r.Version != nil {
				version = *r.Version
			}
			var updatedAt time.Time
			if r.UpdatedAt > 0 {
				updatedAt = time.UnixMilli(r.UpdatedAt).UTC()
			}
			authorHandle := r.OwnerHandle
			var authorImage string
			if r.Owner != nil {
				if authorHandle == "" {
					authorHandle = r.Owner.Handle
				}
				authorImage = r.Owner.Image
			}
			items = append(items, CatalogSkill{
				Slug:         r.Slug,
				Name:         r.DisplayName,
				Summary:      r.Summary,
				Version:      version,
				UpdatedAt:    updatedAt,
				AuthorHandle: authorHandle,
				AuthorImage:  authorImage,
			})
		}
		return items, "", nil
	}

	// Browse mode: use clawhubListSkills with cursor passthrough.
	listItems, nextCursor, listErr := clawhubListSkills(ctx, limit, pageToken)
	if listErr != nil {
		return nil, "", listErr
	}
	items = make([]CatalogSkill, 0, len(listItems))
	for _, li := range listItems {
		version := li.Tags.Latest
		if version == "" {
			version = li.LatestVersion.Version
		}
		var updatedAt time.Time
		if li.UpdatedAt > 0 {
			updatedAt = time.UnixMilli(li.UpdatedAt).UTC()
		}
		downloads := li.Stats.Downloads
		installs := li.Stats.InstallsCurrent
		items = append(items, CatalogSkill{
			Slug:      li.Slug,
			Name:      li.DisplayName,
			Summary:   li.Summary,
			Version:   version,
			Downloads: &downloads,
			Installs:  &installs,
			UpdatedAt: updatedAt,
		})
	}
	return items, nextCursor, nil
}

// FetchCatalogDetail resolves a skill's metadata and downloads its archive to surface
// the README (SKILL.md) and file list for a marketplace detail view.
func FetchCatalogDetail(ctx context.Context, slug string) (CatalogSkillDetail, error) {
	detail, err := clawhubFetchDetail(ctx, slug)
	if err != nil {
		return CatalogSkillDetail{}, err
	}
	if detail.Skill == nil {
		return CatalogSkillDetail{}, fmt.Errorf("skill %q not found on clawhub", slug)
	}
	var version string
	if detail.LatestVersion != nil {
		version = detail.LatestVersion.Version
	}

	name, files, cleanup, err := clawhubFetchSkillFiles(ctx, slug, version)
	if err != nil {
		return CatalogSkillDetail{}, err
	}
	defer cleanup()

	fileList := make([]string, 0, len(files))
	for f := range files {
		fileList = append(fileList, f)
	}
	sort.Strings(fileList)

	displayName := detail.Skill.DisplayName
	if displayName == "" {
		displayName = name
	}
	return CatalogSkillDetail{
		Slug:    slug,
		Name:    displayName,
		Summary: detail.Skill.Summary,
		Version: version,
		Readme:  files["SKILL.md"],
		Files:   fileList,
	}, nil
}

func clawhubSearch(ctx context.Context, query string, limit int) ([]clawhubSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	client := newClawhubClient()
	var result struct {
		Results []clawhubSearchResult `json:"results"`
	}
	err := clawhubWithFallback(func(base string) (bool, error) {
		resp, err := client.R().
			SetContext(ctx).
			SetQueryParam("q", query).
			SetQueryParam("limit", fmt.Sprintf("%d", limit)).
			SetResult(&result).
			Get(base + "/api/v1/search")
		if err != nil {
			return false, err
		}
		if resp.StatusCode() == 429 {
			return true, nil
		}
		if resp.IsError() {
			return false, fmt.Errorf("clawhub search returned HTTP %d", resp.StatusCode())
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return result.Results, nil
}

func clawhubFetchDetail(ctx context.Context, slug string) (*clawhubSkillDetail, error) {
	client := newClawhubClient()
	var detail clawhubSkillDetail
	err := clawhubWithFallback(func(base string) (bool, error) {
		resp, err := client.R().
			SetContext(ctx).
			SetResult(&detail).
			Get(base + "/api/v1/skills/" + url.PathEscape(slug))
		if err != nil {
			return false, err
		}
		if resp.StatusCode() == 429 {
			return true, nil
		}
		if resp.IsError() {
			return false, fmt.Errorf("clawhub detail returned HTTP %d for %q", resp.StatusCode(), slug)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
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

	client := newClawhubClient()
	var data []byte
	dlErr := clawhubWithFallback(func(base string) (bool, error) {
		resp, err := client.R().
			SetContext(ctx).
			SetQueryParam("slug", slug).
			SetQueryParam("version", version).
			Get(base + "/api/v1/download")
		if err != nil {
			return false, err
		}
		if resp.StatusCode() == 429 {
			return true, nil
		}
		if resp.IsError() {
			return false, fmt.Errorf("clawhub download returned HTTP %d for %q@%s", resp.StatusCode(), slug, version)
		}
		data = resp.Body()
		return false, nil
	})
	if dlErr != nil {
		return "", nil, nil, fmt.Errorf("clawhub download: %w", dlErr)
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
