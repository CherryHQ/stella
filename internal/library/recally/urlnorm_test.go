package recally

import (
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basic URL",
			input:    "https://example.com/path",
			expected: "https://example.com/path",
		},
		{
			name:     "uppercase host",
			input:    "https://EXAMPLE.COM/path",
			expected: "https://example.com/path",
		},
		{
			name:     "mixed case host",
			input:    "https://Example.Com/path",
			expected: "https://example.com/path",
		},
		{
			name:     "utm parameters stripped",
			input:    "https://example.com/article?utm_source=twitter&utm_medium=social&utm_campaign=test",
			expected: "https://example.com/article",
		},
		{
			name:     "fbclid stripped",
			input:    "https://example.com/post?fbclid=IwAR123abc",
			expected: "https://example.com/post",
		},
		{
			name:     "multiple tracking params stripped",
			input:    "https://example.com/page?utm_source=email&fbclid=abc123&gclid=xyz789&foo=bar",
			expected: "https://example.com/page?foo=bar",
		},
		{
			name:     "fragment removed",
			input:    "https://example.com/page#section",
			expected: "https://example.com/page",
		},
		{
			name:     "query params sorted",
			input:    "https://example.com/page?z=last&a=first&m=middle",
			expected: "https://example.com/page?a=first&m=middle&z=last",
		},
		{
			name:     "empty URL",
			input:    "",
			expected: "",
		},
		{
			name:     "URL with port",
			input:    "https://example.com:8080/path",
			expected: "https://example.com:8080/path",
		},
		{
			name:     "complex URL preserved",
			input:    "https://user:pass@example.com:8080/path/to/page?key=value&other=test",
			expected: "https://user:pass@example.com:8080/path/to/page?key=value&other=test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeURL(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractSlug(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple title",
			input:    "Hello World",
			expected: "hello-world",
		},
		{
			name:     "title with punctuation",
			input:    "What's New in Go 1.21?",
			expected: "what-s-new-in-go-1-21",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "untitled",
		},
		{
			name:     "only special chars",
			input:    "!!!???",
			expected: "untitled",
		},
		{
			name:     "long title truncated",
			input:    "This is a very long title that should be truncated because it exceeds the maximum length allowed for a slug in the file system",
			expected: "this-is-a-very-long-title-that-should-be-truncated-because-it-exceeds-the-maximum-length-allowed",
		},
		{
			name:     "already lowercase",
			input:    "simple article title",
			expected: "simple-article-title",
		},
		{
			name:     "numbers preserved",
			input:    "Article 123 Test",
			expected: "article-123-test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractSlug(tt.input)
			if result != tt.expected {
				t.Errorf("ExtractSlug(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
