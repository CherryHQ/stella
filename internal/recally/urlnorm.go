// Package recally provides deterministic URL normalization helpers.
package recally

import (
	"net/url"
	"sort"
	"strings"
)

var trackingParams = map[string]bool{
	"utm_source":   true,
	"utm_medium":   true,
	"utm_campaign": true,
	"utm_term":     true,
	"utm_content":  true,
	"gclid":        true,
	"dclid":        true,
	"fbclid":       true,
	"fb_source":    true,
	"twclid":       true,
	"li_fat_id":    true,
	"msclkid":      true,
	"mc_cid":       true,
	"mc_eid":       true,
	"srsltid":      true,
	"ef_id":        true,
	"pk_campaign":  true,
	"pk_kwd":       true,
	"pk_keyword":   true,
	"pk_source":    true,
	"pk_medium":    true,
	"pk_content":   true,
}

// NormalizeURL performs deterministic normalization with no network access.
func NormalizeURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return strings.ToLower(rawURL)
	}

	u.Host = strings.ToLower(u.Host)
	if u.RawQuery != "" {
		values, err := url.ParseQuery(u.RawQuery)
		if err == nil {
			filtered := make(url.Values)
			for key, vals := range values {
				if trackingParams[strings.ToLower(key)] {
					continue
				}
				filtered[key] = vals
			}
			u.RawQuery = encodeSortedQuery(filtered)
		}
	}

	if u.Fragment != "" && !isHashRoute(u.Fragment) {
		u.Fragment = ""
		u.RawFragment = ""
	}

	return u.String()
}

func encodeSortedQuery(v url.Values) string {
	if len(v) == 0 {
		return ""
	}

	keys := make([]string, 0, len(v))
	for key := range v {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buf strings.Builder
	for _, key := range keys {
		values := append([]string(nil), v[key]...)
		sort.Strings(values)
		for _, value := range values {
			if buf.Len() > 0 {
				buf.WriteByte('&')
			}
			buf.WriteString(url.QueryEscape(key))
			if value != "" {
				buf.WriteByte('=')
				buf.WriteString(url.QueryEscape(value))
			}
		}
	}
	return buf.String()
}

func isHashRoute(fragment string) bool {
	return strings.HasPrefix(fragment, "!/") || strings.HasPrefix(fragment, "/")
}

// ExtractSlug generates a filesystem-safe slug from a title.
func ExtractSlug(value string) string {
	if value == "" {
		return "untitled"
	}

	var buf strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			buf.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen {
				buf.WriteByte('-')
				lastHyphen = true
			}
		}
	}

	slug := strings.Trim(buf.String(), "-")
	if slug == "" {
		return "untitled"
	}
	if len(slug) > 100 {
		trimmed := slug[:100]
		if idx := strings.LastIndex(trimmed, "-"); idx > 0 {
			trimmed = trimmed[:idx]
		}
		slug = strings.TrimRight(trimmed, "-")
	}
	if slug == "" {
		return "untitled"
	}
	return slug
}
