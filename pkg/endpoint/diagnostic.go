// Package endpoint formats service endpoints for diagnostic output.
package endpoint

import (
	"net/url"
	"strings"
)

const invalid = "[invalid endpoint]"

// ForDiagnostic retains the scheme, host, and path of a parseable endpoint, but
// removes URL components that routinely carry credentials. Invalid endpoints are
// not echoed: a malformed value must not turn an error path into a secret leak.
func ForDiagnostic(raw string) string {
	u, authority, err := parse(raw)
	if err != nil {
		return invalid
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	out := u.String()
	if authority {
		return strings.TrimPrefix(out, "//")
	}
	return out
}

// parse accepts the authority-form host:port values used by OTLP/gRPC as well
// as normal URLs. Prefixing authority-form input makes net/url apply the same
// userinfo/query/fragment parsing without mistaking a port for a URL scheme.
func parse(raw string) (*url.URL, bool, error) {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		return u, false, err
	}
	u, err := url.Parse("//" + raw)
	return u, true, err
}
