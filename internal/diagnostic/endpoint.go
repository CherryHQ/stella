// Package diagnostic renders potentially sensitive values for operator output.
package diagnostic

import (
	"net/url"
	"strings"
)

const invalidEndpoint = "[invalid endpoint]"

// Endpoint retains the scheme, host, and path of a parseable endpoint, but
// removes URL components that routinely carry credentials. Invalid endpoints
// are not echoed: an error path must not become a secret leak.
func Endpoint(raw string) string {
	u, authority, err := parseEndpoint(raw)
	if err != nil {
		return invalidEndpoint
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

// parseEndpoint accepts the authority-form host:port values used by OTLP/gRPC
// as well as normal URLs. Prefixing authority-form input makes net/url apply the
// same userinfo/query/fragment parsing without mistaking a port for a URL scheme.
func parseEndpoint(raw string) (*url.URL, bool, error) {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		return u, false, err
	}
	u, err := url.Parse("//" + raw)
	return u, true, err
}
