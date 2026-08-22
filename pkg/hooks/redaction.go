package hooks

import "regexp"

// Credential shapes scrubbed from tool input and result before either the
// trace hook or an in-process execution bridge can retain them. Keep this in
// hooks, the shared tool boundary, so code mode cannot accidentally grow a
// second, divergent secret filter.
var (
	reURLCreds     = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+):[^\s:/@]+@`)
	reAuthScheme   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+\S+`)
	reCookie       = regexp.MustCompile(`(?i)(set-)?cookie(["']?\s*[:=]\s*)["']?[^\r\n"']+`)
	reSecretAssign = regexp.MustCompile(`(?i)([a-z0-9_-]*(?:api[_-]?key|secret|token|password|passwd|pwd))(["']?\s*[:=]\s*["']?)\S+`)
	reBareToken    = regexp.MustCompile(`(?i)\b(sk|pk|rk|ghp|gho|ghs|xox[bpas])[-_][A-Za-z0-9_-]{12,}`)
)

// RedactToolText masks credential-like substrings before tool data crosses a
// durable or script-visible boundary. It is best-effort, not a replacement for
// capability controls.
func RedactToolText(s string) string {
	s = reURLCreds.ReplaceAllString(s, "$1:[REDACTED]@")
	s = reCookie.ReplaceAllString(s, "${1}cookie$2[REDACTED]")
	s = reAuthScheme.ReplaceAllString(s, "$1 [REDACTED]")
	s = reSecretAssign.ReplaceAllString(s, "$1$2[REDACTED]")
	s = reBareToken.ReplaceAllString(s, "[REDACTED]")
	return s
}
