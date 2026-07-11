package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// requireExternalDBEnv makes an empty STELLA_DATABASE_URL a startup error
// instead of silently starting the embedded PostgreSQL cluster. The embedded
// cluster is a single-node local convenience: in a container it lands on an
// ephemeral filesystem, and with multiple replicas each process would create
// its own database. The Docker image sets this by default; set it to 0 to
// deliberately run embedded PostgreSQL inside a container (e.g. a single
// container with a persistent volume).
const requireExternalDBEnv = "STELLA_REQUIRE_EXTERNAL_DB"

// RequireExternalDB reports whether STELLA_REQUIRE_EXTERNAL_DB forbids the
// embedded-PostgreSQL fallback. Unset or empty is false. Any other value is
// parsed as a boolean; an unparseable value returns an actionable error instead
// of being silently ignored, so a typo fails fast rather than quietly disabling
// the guard.
func RequireExternalDB() (bool, error) {
	return parseBoolEnv(requireExternalDBEnv)
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
