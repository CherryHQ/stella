package hooks

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Credential shapes scrubbed from tool input and result before either the
// trace hook or an in-process execution bridge can retain them. Keep this in
// hooks, the shared tool boundary, so code mode cannot accidentally grow a
// second, divergent secret filter.
var (
	reURLCreds     = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+):[^\s:/@]+@`)
	reAuthScheme   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+\S+`)
	reCookie       = regexp.MustCompile(`(?i)(set-)?cookie(["']?\s*[:=]\s*)["']?[^\r\n"']+`)
	reSecretAssign = regexp.MustCompile(`(?i)([a-z0-9_-]*(?:api[_-]?key|secret|token|password|passwd|pwd))(["']?\s*[:=]\s*["']?)\S+`)
	reBareToken    = regexp.MustCompile(`(?i)\b(sk|pk|rk|ghp|gho|ghs|github_pat|xox[bpas])[-_][A-Za-z0-9_-]{12,}`)
	reSecretKey    = regexp.MustCompile(`(?i)(?:^|[_-])(?:api[_-]?key|secret|token|password|passwd|pwd)$`)
)

// RedactToolText masks credential-like substrings before tool data crosses a
// durable or script-visible boundary. It is best-effort, not a replacement for
// capability controls.
// RedactSecretValues removes exact runtime secret values, longest first so a
// shorter value cannot partially reveal a longer credential. Callers supply a
// snapshot; this function never retains it.
func RedactSecretValues(s string, values []string) string {
	values = append([]string(nil), values...)
	values = slices.DeleteFunc(values, func(value string) bool { return value == "" })
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		s = strings.ReplaceAll(s, value, "[REDACTED_SECRET]")
	}
	return s
}

func RedactToolText(s string) string {
	decoder := json.NewDecoder(bytes.NewBufferString(s))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) == nil && decoder.Decode(&struct{}{}) == io.EOF {
		if redacted, err := marshalJSONText(redactJSONValue(value, "")); err == nil {
			return redacted
		}
	}
	return redactFreeText(s)
}

func marshalJSONText(value any) (string, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}

func redactJSONValue(value any, key string) any {
	switch value := value.(type) {
	case map[string]any:
		for childKey, child := range value {
			value[childKey] = redactJSONValue(child, childKey)
		}
		return value
	case []any:
		for i, child := range value {
			value[i] = redactJSONValue(child, key)
		}
		return value
	case string:
		normalizedKey := normalizeSecretKey(key)
		if reSecretKey.MatchString(normalizedKey) && !isPaginationTokenKey(normalizedKey) {
			return "[REDACTED]"
		}
		return redactFreeText(value)
	default:
		return value
	}
}

func normalizeSecretKey(key string) string {
	runes := []rune(key)
	var out strings.Builder
	for index, r := range runes {
		if r == '-' {
			r = '_'
		}
		if unicode.IsUpper(r) {
			previousLower := index > 0 && (unicode.IsLower(runes[index-1]) || unicode.IsDigit(runes[index-1]))
			acronymBoundary := index > 0 && unicode.IsUpper(runes[index-1]) && index+1 < len(runes) && unicode.IsLower(runes[index+1])
			if previousLower || acronymBoundary {
				out.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		out.WriteRune(r)
	}
	return out.String()
}

func isPaginationTokenKey(key string) bool {
	key = strings.ToLower(key)
	return key == "page_token" || key == "next_page_token"
}

func redactFreeText(s string) string {
	s = reURLCreds.ReplaceAllString(s, "$1:[REDACTED]@")
	s = reCookie.ReplaceAllString(s, "${1}cookie$2[REDACTED]")
	s = reAuthScheme.ReplaceAllString(s, "$1 [REDACTED]")
	s = reSecretAssign.ReplaceAllString(s, "$1$2[REDACTED]")
	s = reBareToken.ReplaceAllString(s, "[REDACTED]")
	return s
}
