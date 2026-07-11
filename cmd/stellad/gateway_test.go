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
