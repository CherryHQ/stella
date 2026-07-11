package main

import "testing"

func TestBaseURLUnsafe(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"public https", "https://stella.example.com", false},
		{"public http with port", "http://stella.example.com:8080", false},
		{"public ip", "http://203.0.113.7:25678", false},
		{"loopback ipv4", "http://127.0.0.1:25678", true},
		{"loopback ipv4 range", "http://127.0.0.53:25678", true},
		{"loopback ipv6", "http://[::1]:25678", true},
		{"localhost", "http://localhost:25678", true},
		{"localhost uppercase", "http://LOCALHOST:25678", true},
		{"unspecified ipv4", "http://0.0.0.0:25678", true},
		{"unspecified ipv6", "http://[::]:25678", true},
		{"non-http scheme", "ftp://stella.example.com", true},
		{"missing scheme", "stella.example.com", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := baseURLUnsafe(tc.url); got != tc.want {
				t.Errorf("baseURLUnsafe(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestCheckDeploymentBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		strict  string
		allow   string
		wantErr bool
	}{
		{"safe url passes regardless", "https://stella.example.com", "1", "", false},
		{"unsafe non-strict warns only", "http://127.0.0.1:25678", "", "", false},
		{"unsafe strict fails", "http://127.0.0.1:25678", "1", "", true},
		{"unsafe strict with override passes", "http://127.0.0.1:25678", "1", "1", false},
		{"strict parse error surfaces", "http://127.0.0.1:25678", "maybe", "", true},
		{"override parse error surfaces", "http://127.0.0.1:25678", "1", "maybe", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STELLA_STRICT_DEPLOYMENT", tc.strict)
			t.Setenv("STELLA_ALLOW_UNSAFE_BASE_URL", tc.allow)
			err := checkDeploymentBaseURL(tc.url)
			if tc.wantErr && err == nil {
				t.Fatalf("checkDeploymentBaseURL(%q) = nil, want error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkDeploymentBaseURL(%q) unexpected error: %v", tc.url, err)
			}
		})
	}
}
