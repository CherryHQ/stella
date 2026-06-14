package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeListResponse is the shape returned by the upstream /api/v1/skills endpoint.
type fakeListResponse struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// fakeSearchResponse is the shape returned by the upstream /api/v1/search endpoint.
type fakeSearchResponse struct {
	Results []map[string]any `json:"results"`
}

func newFakeClawhubServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/skills", func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		var resp fakeListResponse
		if cursor == "" {
			resp = fakeListResponse{
				Items: []map[string]any{
					{
						"slug":        "awesome-skill",
						"displayName": "Awesome Skill",
						"summary":     "Does awesome things",
						"tags":        map[string]any{"latest": "1.2.3"},
						"stats": map[string]any{
							"downloads":       42000,
							"installsCurrent": 500,
							"installsAllTime": 600,
							"stars":           100,
						},
						"updatedAt":     int64(1780785432794),
						"latestVersion": map[string]any{"version": "1.2.3"},
					},
				},
				NextCursor: "cursor-page2",
			}
		} else {
			// Second page — no next cursor.
			resp = fakeListResponse{
				Items: []map[string]any{
					{
						"slug":        "second-skill",
						"displayName": "Second Skill",
						"summary":     "Page 2 skill",
						"tags":        map[string]any{"latest": "0.1.0"},
						"stats": map[string]any{
							"downloads":       100,
							"installsCurrent": 10,
							"installsAllTime": 20,
							"stars":           5,
						},
						"updatedAt":     int64(1767632598365),
						"latestVersion": map[string]any{"version": "0.1.0"},
					},
				},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		resp := fakeSearchResponse{
			Results: []map[string]any{
				{
					"score":       2.5,
					"slug":        "found-skill",
					"displayName": "Found Skill",
					"summary":     "A searched skill",
					"version":     nil,
					"updatedAt":   int64(1778988067226),
					"ownerHandle": "brennerspear",
					"owner": map[string]any{
						"handle":      "brennerspear",
						"displayName": "Brenner Spear",
						"image":       "https://example.com/avatar.png",
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestBrowseCatalog_Browse_MapsStatsAndCursor(t *testing.T) {
	srv := newFakeClawhubServer(t)
	t.Setenv("CLAWHUB_URL", srv.URL)

	items, nextToken, err := BrowseCatalog(context.Background(), "", 10, "")
	if err != nil {
		t.Fatalf("BrowseCatalog browse: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	sk := items[0]
	if sk.Slug != "awesome-skill" {
		t.Errorf("Slug = %q, want %q", sk.Slug, "awesome-skill")
	}
	if sk.Name != "Awesome Skill" {
		t.Errorf("Name = %q, want %q", sk.Name, "Awesome Skill")
	}
	if sk.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", sk.Version, "1.2.3")
	}
	if sk.Downloads == nil || *sk.Downloads != 42000 {
		t.Errorf("Downloads = %v, want 42000", sk.Downloads)
	}
	if sk.Installs == nil || *sk.Installs != 500 {
		t.Errorf("Installs = %v, want 500", sk.Installs)
	}
	if sk.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
	if sk.AuthorHandle != "" {
		t.Errorf("AuthorHandle should be empty in browse mode, got %q", sk.AuthorHandle)
	}
	if nextToken != "cursor-page2" {
		t.Errorf("nextPageToken = %q, want %q", nextToken, "cursor-page2")
	}
}

func TestBrowseCatalog_Browse_PageTokenPassthrough(t *testing.T) {
	var capturedCursor string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills", func(w http.ResponseWriter, r *http.Request) {
		capturedCursor = r.URL.Query().Get("cursor")
		resp := fakeListResponse{Items: []map[string]any{}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("CLAWHUB_URL", srv.URL)

	_, _, err := BrowseCatalog(context.Background(), "", 5, "my-opaque-cursor")
	if err != nil {
		t.Fatalf("BrowseCatalog: %v", err)
	}
	if capturedCursor != "my-opaque-cursor" {
		t.Errorf("cursor in upstream request = %q, want %q", capturedCursor, "my-opaque-cursor")
	}
}

func TestBrowseCatalog_Search_MapsOwnerAndEmptyNextToken(t *testing.T) {
	srv := newFakeClawhubServer(t)
	t.Setenv("CLAWHUB_URL", srv.URL)

	items, nextToken, err := BrowseCatalog(context.Background(), "something", 10, "")
	if err != nil {
		t.Fatalf("BrowseCatalog search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	sk := items[0]
	if sk.Slug != "found-skill" {
		t.Errorf("Slug = %q, want %q", sk.Slug, "found-skill")
	}
	if sk.AuthorHandle != "brennerspear" {
		t.Errorf("AuthorHandle = %q, want %q", sk.AuthorHandle, "brennerspear")
	}
	if sk.AuthorImage != "https://example.com/avatar.png" {
		t.Errorf("AuthorImage = %q, want %q", sk.AuthorImage, "https://example.com/avatar.png")
	}
	if sk.Downloads != nil {
		t.Errorf("Downloads should be nil in search mode, got %v", sk.Downloads)
	}
	if sk.Installs != nil {
		t.Errorf("Installs should be nil in search mode, got %v", sk.Installs)
	}
	if sk.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
	if nextToken != "" {
		t.Errorf("nextPageToken should be empty in search mode, got %q", nextToken)
	}
}

func TestBrowseCatalog_Browse_SecondPageNoNextToken(t *testing.T) {
	srv := newFakeClawhubServer(t)
	t.Setenv("CLAWHUB_URL", srv.URL)

	items, nextToken, err := BrowseCatalog(context.Background(), "", 10, "cursor-page2")
	if err != nil {
		t.Fatalf("BrowseCatalog page 2: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item on page 2, got %d", len(items))
	}
	if items[0].Slug != "second-skill" {
		t.Errorf("Slug = %q, want %q", items[0].Slug, "second-skill")
	}
	if nextToken != "" {
		t.Errorf("nextToken on last page = %q, want empty", nextToken)
	}
}
