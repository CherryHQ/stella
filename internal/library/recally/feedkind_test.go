package recally

import "testing"

func TestSniffFeedKind(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want FeedKind
	}{
		{"x.com profile", "https://x.com/jack", FeedKindTwitter},
		{"twitter.com profile", "https://twitter.com/jack", FeedKindTwitter},
		{"www.x.com", "https://www.x.com/jack", FeedKindTwitter},
		{"mobile.twitter.com", "https://mobile.twitter.com/jack", FeedKindTwitter},
		{"trailing whitespace", "  https://x.com/jack  ", FeedKindTwitter},
		{"rss feed", "https://example.com/feed.xml", FeedKindRSS},
		{"youtube rss", "https://www.youtube.com/feeds/videos.xml?channel_id=abc", FeedKindRSS},
		{"lookalike host is not twitter", "https://notx.com/jack", FeedKindRSS},
		{"empty url", "", FeedKindRSS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SniffFeedKind(tt.url); got != tt.want {
				t.Errorf("SniffFeedKind(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestFeedKindValid(t *testing.T) {
	for _, k := range []FeedKind{FeedKindRSS, FeedKindTwitter, FeedKindWebsite} {
		if !k.Valid() {
			t.Errorf("FeedKind(%q).Valid() = false, want true", k)
		}
	}
	if FeedKind("bogus").Valid() {
		t.Errorf("FeedKind(\"bogus\").Valid() = true, want false")
	}
}

func TestValidateFeedSubscription(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		kind    FeedKind
		wantErr bool
	}{
		{"x profile", "https://x.com/jack", FeedKindTwitter, false},
		{"twitter profile", "https://twitter.com/jack", FeedKindTwitter, false},
		{"x profile trailing slash", "https://x.com/jack/", FeedKindTwitter, false},
		{"x profile with @", "https://x.com/@jack", FeedKindTwitter, false},
		{"x profile trailing whitespace", "  https://x.com/jack  ", FeedKindTwitter, false},
		{"x lists rejected", "https://x.com/i/lists/123", FeedKindTwitter, true},
		{"x handle lists rejected", "https://x.com/jack/lists/123", FeedKindTwitter, true},
		{"x status rejected", "https://x.com/jack/status/1", FeedKindTwitter, true},
		{"x with_replies rejected", "https://x.com/jack/with_replies", FeedKindTwitter, true},
		{"x search rejected", "https://x.com/search?q=go", FeedKindTwitter, true},
		{"x home rejected", "https://x.com/home", FeedKindTwitter, true},
		{"x bookmarks rejected", "https://x.com/i/bookmarks", FeedKindTwitter, true},
		{"x root rejected", "https://x.com/", FeedKindTwitter, true},
		// X hosts are rejected even when kind sniffed to rss — they never serve RSS.
		{"x lists rejected as rss", "https://x.com/i/lists/123", FeedKindRSS, true},
		{"x search rejected as rss", "https://x.com/search?q=go", FeedKindRSS, true},
		// kind=twitter forced on a non-X host is rejected.
		{"non-x host forced twitter", "https://example.com/feed.xml", FeedKindTwitter, true},
		// RSS feeds: any non-X URL accepted (validated at fetch time).
		{"rss feed", "https://example.com/feed.xml", FeedKindRSS, false},
		{"youtube rss", "https://www.youtube.com/feeds/videos.xml?channel_id=abc", FeedKindRSS, false},
		{"lookalike host", "https://notx.com/i/lists/1", FeedKindRSS, false},
		// website: any non-X page accepted (skill scrapes it).
		{"website page", "https://example.com/blog", FeedKindWebsite, false},
		{"website index with path", "https://example.com/news/latest", FeedKindWebsite, false},
		// website on an X host is still blocked by the X-host guard.
		{"website on x lists rejected", "https://x.com/i/lists/1", FeedKindWebsite, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFeedSubscription(tt.url, tt.kind)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateFeedSubscription(%q, %q) error = %v, wantErr %v", tt.url, tt.kind, err, tt.wantErr)
			}
		})
	}
}
