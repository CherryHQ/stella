package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// strictDeploymentEnv marks a managed deployment (Kubernetes and similar
// orchestrators) where the single-node local conveniences are unsafe. When set
// to a truthy value the server refuses fallbacks meant for local use: it
// requires an external PostgreSQL via STELLA_DATABASE_URL instead of the
// embedded cluster, and it requires an explicit, non-loopback STELLA_BASE_URL
// so OAuth callbacks and channel deep links point at a reachable address.
const strictDeploymentEnv = "STELLA_STRICT_DEPLOYMENT"

// allowUnsafeBaseURLEnv opts out of the strict base-URL check: it lets a managed
// deployment start with a loopback or otherwise unsafe STELLA_BASE_URL,
// downgrading the hard failure to a loud warning. Use it only when link-out
// features (OAuth callbacks, channel deep links) are known to be unused.
const allowUnsafeBaseURLEnv = "STELLA_ALLOW_UNSAFE_BASE_URL"

// StrictDeployment reports whether STELLA_STRICT_DEPLOYMENT requests managed
// deployment mode. Unset or empty is false. Any other value is parsed as a
// boolean; an unparseable value returns an actionable error instead of being
// silently ignored, so a typo fails fast rather than quietly disabling the
// guard.
func StrictDeployment() (bool, error) {
	return parseBoolEnv(strictDeploymentEnv)
}

// AllowUnsafeBaseURL reports whether STELLA_ALLOW_UNSAFE_BASE_URL permits an
// unsafe base URL in strict deployment mode. Same parsing rules as
// StrictDeployment.
func AllowUnsafeBaseURL() (bool, error) {
	return parseBoolEnv(allowUnsafeBaseURLEnv)
}

func parseBoolEnv(name string) (bool, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a boolean: set it to 1/true or 0/false", name, v)
	}
	return b, nil
}
