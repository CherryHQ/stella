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
